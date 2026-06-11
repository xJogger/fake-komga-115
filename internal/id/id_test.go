package id

import "testing"

func TestEncodeDecode(t *testing.T) {
	for _, value := range []string{"0", "123456789", "中文 cid"} {
		library, got, err := DecodeSeries(Series("library", value))
		if err != nil || library != "library" || got != value {
			t.Fatalf("round trip %q: got %q/%q, err=%v", value, library, got, err)
		}
	}
}

func TestOneShotSeriesUsesFileIdentity(t *testing.T) {
	library, cid, err := DecodeSeries(OneShotSeries("library", "file-id"))
	if err != nil || library != "library" || cid != "file:file-id" {
		t.Fatalf("got %q/%q, err=%v", library, cid, err)
	}
}
