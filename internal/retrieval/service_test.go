package retrieval

import "testing"

func TestMinimumMatchedTerms(t *testing.T) {
	tests := []struct {
		termCount int
		want      int
	}{
		{termCount: 0, want: 0},
		{termCount: 1, want: 1},
		{termCount: 2, want: 1},
		{termCount: 3, want: 2},
		{termCount: 4, want: 2},
		{termCount: 5, want: 3},
		{termCount: 8, want: 4},
	}
	for _, test := range tests {
		if got := minimumMatchedTerms(test.termCount); got != test.want {
			t.Fatalf("minimumMatchedTerms(%d) = %d, want %d", test.termCount, got, test.want)
		}
	}
}
