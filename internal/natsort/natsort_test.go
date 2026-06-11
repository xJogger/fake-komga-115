package natsort

import (
	"slices"
	"testing"
)

func TestLess(t *testing.T) {
	values := []string{"10.jpg", "2.jpg", "1.jpg", "001a.jpg"}
	slices.SortFunc(values, func(a, b string) int {
		if Less(a, b) {
			return -1
		}
		if Less(b, a) {
			return 1
		}
		return 0
	})
	want := []string{"1.jpg", "001a.jpg", "2.jpg", "10.jpg"}
	if !slices.Equal(values, want) {
		t.Fatalf("got %v want %v", values, want)
	}
}
