import (
	"testing"

	"github.com/pospispa/kubernetes/pkg/api/v1"
)

func TestValidatePVCSelector(t *testing.T) {
	functionUnderTest := "validatePVCSelector"
	// First part: want no error
	succTests := []struct {
		pvc       v1.PersistentVolumeClaim
		wantEmpty bool
	}{
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
			},
			wantEmpty: true,
		},
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
				Spec: v1.PersistentVolumeClaimSpec{
					Selector: nil,
				},
			},
			wantEmpty: true,
		},
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
				Spec: v1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{},
					},
				},
			},
			wantEmpty: true,
		},
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
				Spec: v1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{},
					},
				},
			},
			wantEmpty: true,
		},
		// START OMIT
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
				Spec: v1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      metav1.LabelZoneFailureDomain,
								Operator: metav1.LabelSelectorOpIn,
								Values:   []string{"us-east-1a", "us-east-1b"},
							},
						},
					},
				},
			},
			wantEmpty: false,
		},
		// END OMIT
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
				Spec: v1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      metav1.LabelZoneRegion,
								Operator: metav1.LabelSelectorOpNotIn,
								Values:   []string{"us-east-1a", "us-east-1b"},
							},
						},
					},
				},
			},
			wantEmpty: false,
		},
		{
			pvc: v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
				Spec: v1.PersistentVolumeClaimSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{metav1.LabelZoneFailureDomain: "us-east-1a"},
					},
				},
			},
			wantEmpty: false,
		},
	}
	for _, succTest := range succTests {
		if empty, err := validatePVCSelector(&succTest.pvc); err != nil {
			t.Errorf("%v(%v) returned (%v, %v), want (%v, %v)", functionUnderTest, succTest.pvc, empty, err.Error(), succTest.wantEmpty, nil)
		} else if empty != succTest.wantEmpty {
			t.Errorf("%v(%v) returned (%v, %v), want (%v, %v)", functionUnderTest, succTest.pvc, empty, err, succTest.wantEmpty, nil)
		}
	}

	// Second part: want an error
	errCases := []v1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
			Spec: v1.PersistentVolumeClaimSpec{
				Selector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "key2",
							Operator: "In",
							Values:   []string{"value1", "value2"},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
			Spec: v1.PersistentVolumeClaimSpec{
				Selector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      metav1.LabelZoneFailureDomain,
							Operator: metav1.LabelSelectorOpExists,
							Values:   []string{"value1", "value2"},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
			Spec: v1.PersistentVolumeClaimSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "foo"},
			Spec: v1.PersistentVolumeClaimSpec{
				Selector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      metav1.LabelZoneFailureDomain,
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{},
						},
					},
				},
			},
		},
	}
	for _, errCase := range errCases {
		if empty, err := validatePVCSelector(&errCase); err == nil {
			t.Errorf("%v(%v) returned (%v, %v), want (%v, %v)", functionUnderTest, errCase, empty, err, false, "an error")
		} else if empty != false {
			t.Errorf("%v(%v) returned (%v, %v), want (%v, %v)", functionUnderTest, errCase, empty, err.Error(), false, "an error")
		}
	}
}
