package localfs

import (
	"errors"
	"testing"
	"time"
)

type fixtureDocuments struct {
	opens int
	block bool
}

func (d *fixtureDocuments) List(root, rel string) (string, error) {
	if d.block {
		return "", errors.New("permission revoked")
	}
	return `[{"ID":"folder","Name":"Series","IsDir":true},{"ID":"comic","Name":"Volume.cbz","Size":1234,"MTime":1700000000000},{"ID":"invalid","Name":"../escape.cbz"}]`, nil
}

func (d *fixtureDocuments) Open(root, rel string) (int32, error) {
	d.opens++
	return -1, errors.New("cannot open fixture")
}

func TestDocumentDirectoryOnlyListsMetadata(t *testing.T) {
	root := "content://test.invalid/tree/fixture"
	if _, err := CleanRoot(root); err == nil {
		t.Fatal("CLI must reject Android documents without a backend")
	}
	d := &fixtureDocuments{}
	SetDocumentBackend(d)
	t.Cleanup(func() { SetDocumentBackend(nil) })
	if _, err := CleanRoot(root); err != nil {
		t.Fatal(err)
	}
	entries, err := ListDirectory(root, "parent")
	if err != nil || len(entries) != 2 || d.opens != 0 {
		t.Fatalf("list: %v, %v (opens=%d)", entries, err, d.opens)
	}
	if !entries[0].IsDir || entries[1].RelPath != "parent/Volume.cbz" ||
		entries[1].Size != 1234 || !entries[1].ModifiedAt.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("metadata did not round trip: %v", entries)
	}
	if _, err := ListDirectory(root, "../outside"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("unsafe listing: %v", err)
	}
	if _, err := OpenBook(root, "../outside.cbz"); !errors.Is(err, ErrPathOutsideRoot) || d.opens != 0 {
		t.Fatalf("unsafe read: %v", err)
	}
	d.block = true
	if _, err := CleanRoot(root); err == nil {
		t.Fatal("revoked SAF permission must be an error, not an empty listing")
	}
}
