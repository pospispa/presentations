package potter

import "testing"

func cost(amount int) int {
	return amount * 8
}

func Test_zero(t *testing.T) {
	// START OMIT
	tests := []struct { // HL
		in   int // HL
		want int // HL
	}{ // HL
		{0, 0}, // HL
		{1, 8}, // HL
	} // HL
	for _, tt := range tests {
		if got := cost(tt.in); got != tt.want {
			t.Errorf("cost(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
	// END OMIT
}
