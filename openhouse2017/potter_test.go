package potter

import "testing"

func cost(amount int) int {
	return amount * 8
}

func Test_zero(t *testing.T) {
	// START OMIT
	tests := []struct {
		in   int
		want int
	}{
		{0, 0},
		{1, 8},
	}
	for _, tt := range tests {
		if got := cost(tt.in); got != tt.want {
			t.Errorf("cost(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
	// END OMIT
}
