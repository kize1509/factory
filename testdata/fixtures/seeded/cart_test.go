package seeded

import "testing"

func TestTotal(t *testing.T) {
	if got := Total([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Total = %d, want 6", got)
	}
}

func TestCount(t *testing.T) {
	if Count([]int{1, 2}) != 2 {
		t.Fatal("count")
	}
}
