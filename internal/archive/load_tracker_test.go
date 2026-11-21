package archive

import (
	"bytes"
	"context"
	"testing"
)

func TestLoadTrackerLifecycleAndFallback(t *testing.T) {
	tr := newLoadTracker()
	if st := tr.Status("book1", 0); st.Active || st.Stage != "idle" || st.BookID != "book1" {
		t.Fatalf("unexpected initial idle status: %+v", st)
	}

	idxCtx, idxDone := tr.begin(context.Background(), "book1", 0, "indexing", "正在解析归档目录…", 1024)
	reportLoadProvider(idxCtx, "webdav")
	reportLoadBytes(idxCtx, 512, 0)
	reportLoadPagesFound(idxCtx, 42)

	// Querying page status while indexing is active falls back to indexing state.
	stPageFallback := tr.Status("book1", 3)
	if !stPageFallback.Active || stPageFallback.Stage != "indexing" || stPageFallback.Provider != "webdav" || stPageFallback.BytesLoaded != 512 || stPageFallback.BytesTotal != 1024 || stPageFallback.PagesFound != 42 {
		t.Fatalf("unexpected fallback status: %+v", stPageFallback)
	}

	// Page-specific active load takes precedence over book-level index load.
	pageCtx, pageDone := tr.begin(context.Background(), "book1", 3, "downloading", "正在拉取分块…", 4096)
	reportLoadProvider(pageCtx, "115")
	reportLoadStage(pageCtx, "decompressing", "正在解压页面…")
	data, err := readAllReporting(pageCtx, bytes.NewReader(make([]byte, 1500)))
	if err != nil || len(data) != 1500 {
		t.Fatalf("readAllReporting len=%d err=%v", len(data), err)
	}

	stPage := tr.Status("book1", 3)
	if !stPage.Active || stPage.Stage != "decompressing" || stPage.Provider != "115" || stPage.PageNumber != 3 || stPage.BytesLoaded != 1500 || stPage.BytesTotal != 4096 {
		t.Fatalf("unexpected page status: %+v", stPage)
	}

	pageDone()
	idxDone()

	if st := tr.Status("book1", 3); st.Active || st.Stage != "idle" {
		t.Fatalf("expected idle after cleanup, got %+v", st)
	}
}
