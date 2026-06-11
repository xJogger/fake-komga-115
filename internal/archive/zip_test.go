package archive

import (
	"testing"

	"github.com/nwaples/rardecode/v2"
)

func TestMimeType(t *testing.T) {
	tests := map[string]string{
		"a.JPG":  "image/jpeg",
		"a.png":  "image/png",
		"a.webp": "image/webp",
		"a.txt":  "",
	}
	for name, want := range tests {
		if got := MimeType(name); got != want {
			t.Fatalf("MimeType(%q)=%q want %q", name, got, want)
		}
	}
}

func TestArchiveMediaType(t *testing.T) {
	tests := map[string]string{
		"book.cbz": "application/zip",
		"book.zip": "application/zip",
		"book.cbr": "application/x-rar-compressed",
		"book.RAR": "application/x-rar-compressed",
		"book.7z":  "application/octet-stream",
	}
	for name, want := range tests {
		if got := MediaType(name); got != want {
			t.Fatalf("MediaType(%q)=%q want %q", name, got, want)
		}
	}
}

func TestRARPageEntries(t *testing.T) {
	files := []*rardecode.File{
		{FileHeader: rardecode.FileHeader{Name: "10.png", PackedSize: 10, UnPackedSize: 20}},
		{FileHeader: rardecode.FileHeader{Name: "notes.txt", PackedSize: 1, UnPackedSize: 1}},
		{FileHeader: rardecode.FileHeader{Name: "2.jpg", PackedSize: 30, UnPackedSize: 40}},
	}
	pages, err := rarPageEntries(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 ||
		pages[0].Name != "2.jpg" || pages[0].ArchiveIndex != 2 || pages[0].Number != 1 ||
		pages[1].Name != "10.png" || pages[1].ArchiveIndex != 0 || pages[1].Number != 2 {
		t.Fatalf("unexpected RAR pages: %+v", pages)
	}
}
