package httpserver

import (
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

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

func TestBookDTOUsesRemoteFileCreationTime(t *testing.T) {
	scannedAt := time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC)
	fileCreatedAt := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	dto := bookDTO(database.Book{
		CreatedAt: scannedAt, UpdatedAt: scannedAt, FileCreatedAt: &fileCreatedAt,
		Name: "book.cbz",
	}, database.Series{})

	want := fileCreatedAt.Format(time.RFC3339)
	if dto["created"] != want {
		t.Fatalf("created=%v want %s", dto["created"], want)
	}
	metadata := dto["metadata"].(map[string]any)
	if metadata["created"] != want {
		t.Fatalf("metadata.created=%v want %s", metadata["created"], want)
	}
}

func TestBookDTOFallsBackToDatabaseCreationTime(t *testing.T) {
	scannedAt := time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC)
	dto := bookDTO(database.Book{
		CreatedAt: scannedAt, UpdatedAt: scannedAt, Name: "book.cbz",
	}, database.Series{})

	if dto["created"] != scannedAt.Format(time.RFC3339) {
		t.Fatalf("created=%v", dto["created"])
	}
}
