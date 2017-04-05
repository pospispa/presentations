package romans

import "errors"

var Invalid = errors.New("not a roman numeral")

func valid(i string) bool {
	if i == "a" {
		return false
	}
	return true
}

//START OMIT
func ToInt(i string) (int, error) {
	if !valid(i) {
		//END OMIT
		return -1, Invalid
	}
	//END OMIT
	m := map[string]int{
		"I": 1,
		"V": 5,
		"X": 10,
		"L": 50,
		"C": 100,
		"D": 500,
		"M": 1000,
	}

	sum := 0
	for j := range i {
		if j < len(i)-1 {
			if m[i[j:j+1]] < m[i[j+1:j+2]] {
				sum = sum - m[i[j:j+1]]
				continue
			}
		}
		sum = sum + m[i[j:j+1]]
	}

	return sum, nil
}
