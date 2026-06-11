package httpserver

import "testing"

func TestMakePage(t *testing.T) {
	page := makePage([]int{3, 4}, 1, 2, 5, false)
	if page.TotalPages != 3 || page.First || page.Last || page.NumberOfElements != 2 {
		t.Fatalf("unexpected page: %+v", page)
	}
	last := makePage([]int{5}, 2, 2, 5, false)
	if !last.Last || last.Empty {
		t.Fatalf("unexpected last page: %+v", last)
	}
}
