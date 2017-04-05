/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package volume

import (
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/client/clientset_generated/clientset"

	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	volutil "k8s.io/kubernetes/pkg/volume/util"
)

type RecycleEventRecorder func(eventtype, message string)

// RecycleVolumeByWatchingPodUntilCompletion is intended for use with volume
// Recyclers. This function will save the given Pod to the API and watch it
// until it completes, fails, or the pod's ActiveDeadlineSeconds is exceeded,
// whichever comes first. An attempt to delete a recycler pod is always
// attempted before returning.
//
// In case there is a pod with the same namespace+name already running, this
// function assumes it's an older instance of the recycler pod and watches
// this old pod instead of starting a new one.
//
//  pod - the pod designed by a volume plugin to recycle the volume. pod.Name
//        will be overwritten with unique name based on PV.Name.
//	client - kube client for API operations.
func RecycleVolumeByWatchingPodUntilCompletion(pvName string, pod *v1.Pod, kubeClient clientset.Interface, recorder RecycleEventRecorder) error {
	return internalRecycleVolumeByWatchingPodUntilCompletion(pvName, pod, newRecyclerClient(kubeClient, recorder))
}

// same as above func comments, except 'recyclerClient' is a narrower pod API
// interface to ease testing
func internalRecycleVolumeByWatchingPodUntilCompletion(pvName string, pod *v1.Pod, recyclerClient recyclerClient) error {
	glog.V(5).Infof("creating recycler pod for volume %s\n", pod.Name)

	// Generate unique name for the recycler pod - we need to get "already
	// exists" error when a previous controller has already started recycling
	// the volume. Here we assume that pv.Name is already unique.
	pod.Name = "recycler-for-" + pvName
	pod.GenerateName = ""

	stopChannel := make(chan struct{})
	defer close(stopChannel)
	podCh, err := recyclerClient.WatchPod(pod.Name, pod.Namespace, stopChannel)
	if err != nil {
		glog.V(4).Infof("cannot start watcher for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return err
	}

	// Start the pod
	_, err = recyclerClient.CreatePod(pod)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			glog.V(5).Infof("old recycler pod %q found for volume", pod.Name)
		} else {
			return fmt.Errorf("unexpected error creating recycler pod:  %+v\n", err)
		}
	}
	defer func(pod *v1.Pod) {
		glog.V(2).Infof("deleting recycler pod %s/%s", pod.Namespace, pod.Name)
		if err := recyclerClient.DeletePod(pod.Name, pod.Namespace); err != nil {
			glog.Errorf("failed to delete recycler pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}
	}(pod)

	// Now only the old pod or the new pod run. Watch it until it finishes
	// and send all events on the pod to the PV
	for {
		event := <-podCh
		switch event.Object.(type) {
		case *v1.Pod:
			// POD changed
			pod := event.Object.(*v1.Pod)
			glog.V(4).Infof("recycler pod update received: %s %s/%s %s", event.Type, pod.Namespace, pod.Name, pod.Status.Phase)
			switch event.Type {
			case watch.Added, watch.Modified:
				if pod.Status.Phase == v1.PodSucceeded {
					// Recycle succeeded.
					return nil
				}
				if pod.Status.Phase == v1.PodFailed {
					if pod.Status.Message != "" {
						return fmt.Errorf(pod.Status.Message)
					} else {
						return fmt.Errorf("pod failed, pod.Status.Message unknown.")
					}
				}

			case watch.Deleted:
				return fmt.Errorf("recycler pod was deleted")

			case watch.Error:
				return fmt.Errorf("recycler pod watcher failed")
			}

		case *v1.Event:
			// Event received
			podEvent := event.Object.(*v1.Event)
			glog.V(4).Infof("recycler event received: %s %s/%s %s/%s %s", event.Type, podEvent.Namespace, podEvent.Name, podEvent.InvolvedObject.Namespace, podEvent.InvolvedObject.Name, podEvent.Message)
			if event.Type == watch.Added {
				recyclerClient.Event(podEvent.Type, podEvent.Message)
			}
		}
	}
}

// recyclerClient abstracts access to a Pod by providing a narrower interface.
// This makes it easier to mock a client for testing.
type recyclerClient interface {
	CreatePod(pod *v1.Pod) (*v1.Pod, error)
	GetPod(name, namespace string) (*v1.Pod, error)
	DeletePod(name, namespace string) error
	// WatchPod returns a ListWatch for watching a pod.  The stopChannel is used
	// to close the reflector backing the watch.  The caller is responsible for
	// derring a close on the channel to stop the reflector.
	WatchPod(name, namespace string, stopChannel chan struct{}) (<-chan watch.Event, error)
	// Event sends an event to the volume that is being recycled.
	Event(eventtype, message string)
}

func newRecyclerClient(client clientset.Interface, recorder RecycleEventRecorder) recyclerClient {
	return &realRecyclerClient{
		client,
		recorder,
	}
}

type realRecyclerClient struct {
	client   clientset.Interface
	recorder RecycleEventRecorder
}

func (c *realRecyclerClient) CreatePod(pod *v1.Pod) (*v1.Pod, error) {
	return c.client.Core().Pods(pod.Namespace).Create(pod)
}

func (c *realRecyclerClient) GetPod(name, namespace string) (*v1.Pod, error) {
	return c.client.Core().Pods(namespace).Get(name, metav1.GetOptions{})
}

func (c *realRecyclerClient) DeletePod(name, namespace string) error {
	return c.client.Core().Pods(namespace).Delete(name, nil)
}

func (c *realRecyclerClient) Event(eventtype, message string) {
	c.recorder(eventtype, message)
}

func (c *realRecyclerClient) WatchPod(name, namespace string, stopChannel chan struct{}) (<-chan watch.Event, error) {
	podSelector, _ := fields.ParseSelector("metadata.name=" + name)
	options := metav1.ListOptions{
		FieldSelector: podSelector.String(),
		Watch:         true,
	}

	podWatch, err := c.client.Core().Pods(namespace).Watch(options)
	if err != nil {
		return nil, err
	}

	eventSelector, _ := fields.ParseSelector("involvedObject.name=" + name)
	eventWatch, err := c.client.Core().Events(namespace).Watch(metav1.ListOptions{
		FieldSelector: eventSelector.String(),
		Watch:         true,
	})
	if err != nil {
		podWatch.Stop()
		return nil, err
	}

	eventCh := make(chan watch.Event, 0)

	go func() {
		defer eventWatch.Stop()
		defer podWatch.Stop()
		defer close(eventCh)

		for {
			select {
			case _ = <-stopChannel:
				return

			case podEvent, ok := <-podWatch.ResultChan():
				if !ok {
					return
				}
				eventCh <- podEvent

			case eventEvent, ok := <-eventWatch.ResultChan():
				if !ok {
					return
				}
				eventCh <- eventEvent
			}
		}
	}()

	return eventCh, nil
}

// CalculateTimeoutForVolume calculates time for a Recycler pod to complete a
// recycle operation. The calculation and return value is either the
// minimumTimeout or the timeoutIncrement per Gi of storage size, whichever is
// greater.
func CalculateTimeoutForVolume(minimumTimeout, timeoutIncrement int, pv *v1.PersistentVolume) int64 {
	giQty := resource.MustParse("1Gi")
	pvQty := pv.Spec.Capacity[v1.ResourceStorage]
	giSize := giQty.Value()
	pvSize := pvQty.Value()
	timeout := (pvSize / giSize) * int64(timeoutIncrement)
	if timeout < int64(minimumTimeout) {
		return int64(minimumTimeout)
	} else {
		return timeout
	}
}

// RoundUpSize calculates how many allocation units are needed to accommodate
// a volume of given size. E.g. when user wants 1500MiB volume, while AWS EBS
// allocates volumes in gibibyte-sized chunks,
// RoundUpSize(1500 * 1024*1024, 1024*1024*1024) returns '2'
// (2 GiB is the smallest allocatable volume that can hold 1500MiB)
func RoundUpSize(volumeSizeBytes int64, allocationUnitBytes int64) int64 {
	return (volumeSizeBytes + allocationUnitBytes - 1) / allocationUnitBytes
}

// GenerateVolumeName returns a PV name with clusterName prefix. The function
// should be used to generate a name of GCE PD or Cinder volume. It basically
// adds "<clusterName>-dynamic-" before the PV name, making sure the resulting
// string fits given length and cuts "dynamic" if not.
func GenerateVolumeName(clusterName, pvName string, maxLength int) string {
	prefix := clusterName + "-dynamic"
	pvLen := len(pvName)

	// cut the "<clusterName>-dynamic" to fit full pvName into maxLength
	// +1 for the '-' dash
	if pvLen+1+len(prefix) > maxLength {
		prefix = prefix[:maxLength-pvLen-1]
	}
	return prefix + "-" + pvName
}

// Check if the path from the mounter is empty.
func GetPath(mounter Mounter) (string, error) {
	path := mounter.GetPath()
	if path == "" {
		return "", fmt.Errorf("Path is empty %s", reflect.TypeOf(mounter).String())
	}
	return path, nil
}

// ChooseZone implements our heuristics for choosing a zone for volume creation based on the volume name
// Volumes are generally round-robin-ed across all active zones, using the hash of the PVC Name.
// However, if the PVCName ends with `-<integer>`, we will hash the prefix, and then add the integer to the hash.
// This means that a StatefulSet's volumes (`claimname-statefulsetname-id`) will spread across available zones,
// assuming the id values are consecutive.
func ChooseZoneForVolume(zones sets.String, pvcName string) string {
	// We create the volume in a zone determined by the name
	// Eventually the scheduler will coordinate placement into an available zone
	var hash uint32
	var index uint32

	if pvcName == "" {
		// We should always be called with a name; this shouldn't happen
		glog.Warningf("No name defined during volume create; choosing random zone")

		hash = rand.Uint32()
	} else {
		hashString := pvcName

		// Heuristic to make sure that volumes in a StatefulSet are spread across zones
		// StatefulSet PVCs are (currently) named ClaimName-StatefulSetName-Id,
		// where Id is an integer index.
		// Note though that if a StatefulSet pod has multiple claims, we need them to be
		// in the same zone, because otherwise the pod will be unable to mount both volumes,
		// and will be unschedulable.  So we hash _only_ the "StatefulSetName" portion when
		// it looks like `ClaimName-StatefulSetName-Id`.
		// We continue to round-robin volume names that look like `Name-Id` also; this is a useful
		// feature for users that are creating statefulset-like functionality without using statefulsets.
		lastDash := strings.LastIndexByte(pvcName, '-')
		if lastDash != -1 {
			statefulsetIDString := pvcName[lastDash+1:]
			statefulsetID, err := strconv.ParseUint(statefulsetIDString, 10, 32)
			if err == nil {
				// Offset by the statefulsetID, so we round-robin across zones
				index = uint32(statefulsetID)
				// We still hash the volume name, but only the prefix
				hashString = pvcName[:lastDash]

				// In the special case where it looks like `ClaimName-StatefulSetName-Id`,
				// hash only the StatefulSetName, so that different claims on the same StatefulSet
				// member end up in the same zone.
				// Note that StatefulSetName (and ClaimName) might themselves both have dashes.
				// We actually just take the portion after the final - of ClaimName-StatefulSetName.
				// For our purposes it doesn't much matter (just suboptimal spreading).
				lastDash := strings.LastIndexByte(hashString, '-')
				if lastDash != -1 {
					hashString = hashString[lastDash+1:]
				}

				glog.V(2).Infof("Detected StatefulSet-style volume name %q; index=%d", pvcName, index)
			}
		}

		// We hash the (base) volume name, so we don't bias towards the first N zones
		h := fnv.New32()
		h.Write([]byte(hashString))
		hash = h.Sum32()
	}

	// Zones.List returns zones in a consistent order (sorted)
	// We do have a potential failure case where volumes will not be properly spread,
	// if the set of zones changes during StatefulSet volume creation.  However, this is
	// probably relatively unlikely because we expect the set of zones to be essentially
	// static for clusters.
	// Hopefully we can address this problem if/when we do full scheduler integration of
	// PVC placement (which could also e.g. avoid putting volumes in overloaded or
	// unhealthy zones)
	zoneSlice := zones.List()
	zone := zoneSlice[(hash+index)%uint32(len(zoneSlice))]

	glog.V(2).Infof("Creating volume for PVC %q; chose zone=%q from zones=%q", pvcName, zone, zoneSlice)
	return zone
}

// UnmountViaEmptyDir delegates the tear down operation for secret, configmap, git_repo and downwardapi
// to empty_dir
func UnmountViaEmptyDir(dir string, host VolumeHost, volName string, volSpec Spec, podUID types.UID) error {
	glog.V(3).Infof("Tearing down volume %v for pod %v at %v", volName, podUID, dir)

	if pathExists, pathErr := volutil.PathExists(dir); pathErr != nil {
		return fmt.Errorf("Error checking if path exists: %v", pathErr)
	} else if !pathExists {
		glog.Warningf("Warning: Unmount skipped because path does not exist: %v", dir)
		return nil
	}

	// Wrap EmptyDir, let it do the teardown.
	wrapped, err := host.NewWrapperUnmounter(volName, volSpec, podUID)
	if err != nil {
		return err
	}
	return wrapped.TearDownAt(dir)
}

// zonesToSet converts a string containing a comma separated list of zones to set
func zonesToSet(zonesString string) (sets.String, error) {
	zonesSlice := strings.Split(zonesString, ",")
	zonesSet := make(sets.String)
	for _, zone := range zonesSlice {
		trimmedZone := strings.TrimSpace(zone)
		if trimmedZone == "" {
			return make(sets.String), fmt.Errorf("comma separated list of zones (%q) must not contain an empty zone", zonesString)
		}
		zonesSet.Insert(trimmedZone)
	}
	return zonesSet, nil
}

// validatePVCSelector validates Selector part of a PVC:
// - in case there is no Selector the PVC is valid
// - makes sure that only allowedKeys are present in the Selector matchLabels part
// - makes sure that only allowedKeys and allowedOperators are present in the Selector matchExpressions part
// Return value:
// - (true, nil) means PVC is valid (error == nil) and there is NO Selector OR (NO matchLabels AND NO matchExpressions) (bool == true)
// - (false, nil) means PVC is valid (error == nil) and there is at least a value in matchLabels or matchExpressions specified (bool == false)
// - (false, error) means PVC is not valid
// - (true, error) shall never happen
func validatePVCSelector(pvc *v1.PersistentVolumeClaim) (bool, error) {
	allowedKeys := map[string]bool{metav1.LabelZoneFailureDomain: true, metav1.LabelZoneRegion: true}
	allowedOperators := map[metav1.LabelSelectorOperator]bool{metav1.LabelSelectorOpIn: true, metav1.LabelSelectorOpNotIn: true}
	if pvc.Spec.Selector == nil {
		return true, nil
	}
	if len(pvc.Spec.Selector.MatchExpressions) < 1 && len(pvc.Spec.Selector.MatchLabels) < 1 {
		return true, nil
	}
	if len(pvc.Spec.Selector.MatchLabels) > 0 {
		for label := range pvc.Spec.Selector.MatchLabels {
			if !allowedKeys[label] {
				return false, fmt.Errorf("key %q is not permitted in selector.matchLabels", label)
			}
		}
	}
	for _, expr := range pvc.Spec.Selector.MatchExpressions {
		if !allowedKeys[expr.Key] {
			return false, fmt.Errorf("key %q is not permitted in selector.matchExpressions", expr.Key)
		}
		if !allowedOperators[expr.Operator] {
			return false, fmt.Errorf("operator %q is not permitted in selector.matchExpressions", expr.Operator)
		}
		if len(expr.Values) < 1 {
			return false, fmt.Errorf("key %q, operator %q pair does not contain any value(s) in selector.matchExpressions", expr.Key, expr.Operator)
		}
	}
	return false, nil
}

// getPVCMatchLabel returns:
// - either (value, nil) for the key from the matchLabels Selector part of the PVC
// - or ("", error) in case the key is missing in the matchLabels Selector part of the PVC
func getPVCMatchLabel(pvc *v1.PersistentVolumeClaim, key string) (string, error) {
	if pvc.Spec.Selector == nil {
		return "", fmt.Errorf("missing selector.matchLabels")
	}
	if value, ok := pvc.Spec.Selector.MatchLabels[key]; ok {
		return value, nil
	}
	return "", fmt.Errorf("key %q not found in selector.matchLabels", key)
}

// getPVCMatchExpression returns:
// - either ([]setOfValues, nil) for all matching (key, operator) from the matchExpressions Selector part of the PVC
// - or ([]emptySet, error) in case the operator or the key is missing in the matchExpressions Selector part of the PVC
// Example:
// selector:
//     matchExpressions:
//       - key: failure-domain.beta.kubernetes.io/zone
//         operator: In
//         values:
//           - us-east-1a
//           - us-east-2a
//           - us-east-3a
//       - key: failure-domain.beta.kubernetes.io/zone
//             operator: In
//             values:
//               - us-east-3a
//               - us-east-4a
// Returns ({sets.String{"us-east-1a": sets.Empty{}, "us-east-2a": sets.Empty{}, "us-east-3a": sets.Empty{}}, sets.String{"us-east-3a": sets.Empty{}, "us-east-4a": sets.Empty{}}}, nil)
func getPVCMatchExpression(pvc *v1.PersistentVolumeClaim, key string, operator metav1.LabelSelectorOperator) ([]sets.String, error) {
	if pvc.Spec.Selector == nil {
		return make([]sets.String, 0), fmt.Errorf("missing selector.matchExpressions")
	}
	if len(pvc.Spec.Selector.MatchExpressions) < 1 {
		return make([]sets.String, 0), fmt.Errorf("key(s), operator(s) and value(s) are missing in selector.matchExpressions")
	}
	capacity := 0
	for _, item := range pvc.Spec.Selector.MatchExpressions {
		if item.Key == key && item.Operator == operator && len(item.Values) > 0 {
			capacity++
		}
	}
	if capacity == 0 {
		return make([]sets.String, 0), fmt.Errorf("operator %q for key %q not found in selector.matchExpressions", key, operator)
	}

	ret := make([]sets.String, 0, capacity)
	index := 0
	for _, item := range pvc.Spec.Selector.MatchExpressions {
		if item.Key == key && item.Operator == operator && len(item.Values) > 0 {
			ret = append(ret, make(sets.String))
			for _, value := range item.Values {
				ret[index].Insert(value)
			}
			index++
		}
	}
	return ret, nil
}

// ZonesConf is a class for calculation of a set of zones that satisfy both admin configured zones and user configured regions and zones
type ZonesConf struct {
	// PVC data structure containing the user configured regions and zones
	PVC *v1.PersistentVolumeClaim
	// a func that returns a set of all available zones
	GetAllZones func() (sets.String, error)
	// a func that converts a zone to a region
	ZoneToRegion func(string) (string, error)
	// is the parameter zone specified in the Storage Class by an admin?
	isSCZoneConfigured bool
	// is the parameter zones specified in the Storage Class by an admin?
	isSCZonesConfigured bool
	// true if the func GetAllZones was already called
	gotAllAvailableZones bool
	// contains the return value of the func GetAllZones call
	allAvailableZones sets.String
	// the set of zones that satisfy both admin configured zones and user configured regions and zones is calculated in the resultingZones
	resultingZones sets.String
	// is the regionToZones map already calculated
	isRegionToZonesMapValid bool
	// maps a single region to a set of all zones that are available in the region
	regionToZonesMap map[string]sets.String
}

// SetZone sets the zone StorageClass parameter configured by an admin and returns:
// - error in case the zones StorageClass parameter was also configured
// - nil the zone StorageClass parameter was successfully set
func (z *ZonesConf) SetZone(zone string) error {
	if z.isSCZonesConfigured {
		return fmt.Errorf("both zone and zones StorageClass parameters must not be used at the same time")
	}
	z.resultingZones = make(sets.String)
	z.resultingZones.Insert(zone)
	z.isSCZoneConfigured = true
	return nil
}

// SetZones sets the zones StorageClass parameter configured by an admin and returns:
// - error in case the zone StorageClass parameter was also configured
// - error in case the zones StorageClass parameter does not contain a comma separated list of zones
// - nil the zones StorageClass parameter was successfully parsed and set
func (z *ZonesConf) SetZones(zones string) error {
	if z.isSCZoneConfigured {
		return fmt.Errorf("both zone and zones StorageClass parameters must not be used at the same time")
	}
	var err error
	if z.resultingZones, err = zonesToSet(zones); err != nil {
		return fmt.Errorf("corresponding storage class error: %v", err.Error())
	}
	z.isSCZonesConfigured = true
	return nil
}

// getAllAvailableZones caches the result of the func GetAllZones call so it returns:
// - cached result stored in z.allAvailableZones
// - error in case the func GetAllZones returned and error
// - the return value of the func GetAllZones call
func (z *ZonesConf) getAllAvailableZones() (sets.String, error) {
	if z.gotAllAvailableZones {
		return z.allAvailableZones, nil
	}
	var err error
	if z.allAvailableZones, err = z.GetAllZones(); err != nil {
		return nil, err
	}
	z.gotAllAvailableZones = true
	return z.allAvailableZones, nil
}

// regionToZones converts a single region into a set of zones
func (z *ZonesConf) regionToZones(region string) (sets.String, error) {
	if !z.isRegionToZonesMapValid {
		if err := z.calculateRegionToZonesMap(); err != nil {
			return nil, err
		}
	}
	return z.regionToZonesMap[region], nil
}

// calculateRegionToZonesMap returns:
// - nil if the z.regionToZonesMap was successfully calculated
// - error if the func GetAllZones or func ZoneToRegion failed
// Currently cloud providers do not provide a func RegionToZone that will return all zones that are available in a given region.
// Thats why the func calculateRegionToZonesMap goes through allAvailableZones and creates a map region -> set of zones that are available in the region.
func (z *ZonesConf) calculateRegionToZonesMap() error {
	if z.isRegionToZonesMapValid {
		return nil
	}
	z.regionToZonesMap = make(map[string]sets.String)
	var err error
	if !z.gotAllAvailableZones {
		if z.allAvailableZones, err = z.getAllAvailableZones(); err != nil {
			return err
		}
	}
	var region string
	for zone := range z.allAvailableZones {
		if region, err = z.ZoneToRegion(zone); err != nil {
			return fmt.Errorf("failed to convert zone (%v) to a region: %v", zone, err)
		}
		if _, ok := z.regionToZonesMap[region]; !ok {
			z.regionToZonesMap[region] = make(sets.String)
		}
		z.regionToZonesMap[region].Insert(zone)
	}
	z.isRegionToZonesMapValid = true
	return nil
}

//START OMIT
// GetConfZones returns:
// - either a set of zones resulting from currently available zones, allowed zone(s) by an admin in the corresponding storage class and zones preferred by the user in the selector part of the PVC
// - or an error in case the resulting set of zones is empty or another error occurred
func (z *ZonesConf) GetConfZones() (sets.String, error) { // HL
	var err error
	if !z.isSCZoneConfigured && !z.isSCZonesConfigured {
		if z.resultingZones, err = z.getAllAvailableZones(); err != nil {
			return nil, err
		}
	} // else z.resultingZones were already set either in z.SetZone() or z.SetZones()
	if emptySelector, err := validatePVCSelector(z.PVC); err != nil {
		return nil, err
	} else if emptySelector {
		return z.resultingZones, nil
	}
	if matchLabelZone, err := getPVCMatchLabel(z.PVC, metav1.LabelZoneFailureDomain); err == nil {
		matchLabelZoneSet := make(sets.String)
		matchLabelZoneSet.Insert(matchLabelZone)
		z.resultingZones = z.resultingZones.Intersection(matchLabelZoneSet)
	}
	//END OMIT
	if matchLabelRegion, err := getPVCMatchLabel(z.PVC, metav1.LabelZoneRegion); err == nil {
		var zones sets.String
		if zones, err = z.regionToZones(matchLabelRegion); err != nil {
			return nil, err
		}
		z.resultingZones = z.resultingZones.Intersection(zones)
	}
	if matchExpressionZoneSets, err := getPVCMatchExpression(z.PVC, metav1.LabelZoneFailureDomain, metav1.LabelSelectorOpIn); err == nil {
		for _, matchExpressionZoneSet := range matchExpressionZoneSets {
			z.resultingZones = z.resultingZones.Intersection(matchExpressionZoneSet)
		}
	}
	if matchExpressionRegionSets, err := getPVCMatchExpression(z.PVC, metav1.LabelZoneRegion, metav1.LabelSelectorOpIn); err == nil {
		if !z.isRegionToZonesMapValid {
			if err = z.calculateRegionToZonesMap(); err != nil {
				return nil, err
			}
		}
		var summedZonesForASetOfRegions sets.String
		for _, matchExpressionRegionSet := range matchExpressionRegionSets {
			summedZonesForASetOfRegions = make(sets.String)
			for region := range matchExpressionRegionSet {
				summedZonesForASetOfRegions = summedZonesForASetOfRegions.Union(z.regionToZonesMap[region])
			}
			z.resultingZones = z.resultingZones.Intersection(summedZonesForASetOfRegions)
		}
	}
	if matchExpressionZoneSets, err := getPVCMatchExpression(z.PVC, metav1.LabelZoneFailureDomain, metav1.LabelSelectorOpNotIn); err == nil {
		for _, matchExpressionZoneSet := range matchExpressionZoneSets {
			z.resultingZones = z.resultingZones.Difference(matchExpressionZoneSet)
		}
	}
	if matchExpressionRegionSets, err := getPVCMatchExpression(z.PVC, metav1.LabelZoneRegion, metav1.LabelSelectorOpNotIn); err == nil {
		if !z.isRegionToZonesMapValid {
			if err = z.calculateRegionToZonesMap(); err != nil {
				return nil, err
			}
		}
		var summedZonesForASetOfRegions sets.String
		for _, matchExpressionRegionSet := range matchExpressionRegionSets {
			summedZonesForASetOfRegions = make(sets.String)
			for region := range matchExpressionRegionSet {
				summedZonesForASetOfRegions = summedZonesForASetOfRegions.Union(z.regionToZonesMap[region])
			}
			z.resultingZones = z.resultingZones.Difference(summedZonesForASetOfRegions)
		}
	}
	if len(z.resultingZones) < 1 {
		return nil, fmt.Errorf("Could not find availability zone: combination of StorageClass parameters and selector of this claim cannot be satisfied by this cluster")
	}

	return z.resultingZones, nil
}
