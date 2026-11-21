package archive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// LoadProgress describes live server-side streaming/indexing progress for a book or page.
// Shared by both the built-in web reader and future native mobile readers without modifying
// standard Komga /pages endpoints.
type LoadProgress struct {
	Active      bool   `json:"active"`
	BookID      string `json:"bookId"`
	Stage       string `json:"stage"` // "idle" | "indexing" | "downloading" | "decompressing"
	Message     string `json:"message,omitempty"`
	Provider    string `json:"provider,omitempty"` // "115" | "webdav" | "local" | "union"
	PageNumber  int    `json:"pageNumber,omitempty"`
	BytesLoaded int64  `json:"bytesLoaded,omitempty"`
	BytesTotal  int64  `json:"bytesTotal,omitempty"`
	PagesFound  int    `json:"pagesFound,omitempty"`
	ElapsedMs   int64  `json:"elapsedMs,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

type loadState struct {
	bookID      string
	stage       string
	message     string
	provider    string
	pageNumber  int
	bytesLoaded atomic.Int64
	bytesTotal  atomic.Int64
	pagesFound  atomic.Int64
	startedAt   time.Time
	updatedAt   time.Time
	active      bool
	mu          sync.RWMutex
}

func (ls *loadState) snapshot() LoadProgress {
	ls.mu.RLock()
	stage := ls.stage
	msg := ls.message
	prov := ls.provider
	pageNum := ls.pageNumber
	started := ls.startedAt
	updated := ls.updatedAt
	active := ls.active
	bookID := ls.bookID
	ls.mu.RUnlock()

	loaded := ls.bytesLoaded.Load()
	total := ls.bytesTotal.Load()
	pages := int(ls.pagesFound.Load())
	var elapsed int64
	if !started.IsZero() {
		elapsed = time.Since(started).Milliseconds()
	}
	return LoadProgress{
		Active:      active,
		BookID:      bookID,
		Stage:       stage,
		Message:     msg,
		Provider:    prov,
		PageNumber:  pageNum,
		BytesLoaded: loaded,
		BytesTotal:  total,
		PagesFound:  pages,
		ElapsedMs:   elapsed,
		UpdatedAt:   updated.UTC().Format(time.RFC3339Nano),
	}
}

type loadTrackerKey struct{}

type LoadTracker struct {
	mu     sync.RWMutex
	states map[string]*loadState
}

func newLoadTracker() *LoadTracker {
	return &LoadTracker{
		states: make(map[string]*loadState),
	}
}

func trackerKey(bookID string, pageNumber int) string {
	if pageNumber > 0 {
		return fmt.Sprintf("%s#%d", bookID, pageNumber)
	}
	return bookID
}

func (t *LoadTracker) begin(ctx context.Context, bookID string, pageNumber int, stage, message string, expectedTotal int64) (context.Context, func()) {
	if t == nil || bookID == "" {
		return ctx, func() {}
	}
	now := time.Now()
	ls := &loadState{
		bookID:     bookID,
		stage:      stage,
		message:    message,
		pageNumber: pageNumber,
		startedAt:  now,
		updatedAt:  now,
		active:     true,
	}
	if expectedTotal > 0 {
		ls.bytesTotal.Store(expectedTotal)
	}
	key := trackerKey(bookID, pageNumber)
	t.mu.Lock()
	t.states[key] = ls
	t.mu.Unlock()

	done := func() {
		ls.mu.Lock()
		ls.active = false
		ls.updatedAt = time.Now()
		ls.mu.Unlock()
		t.mu.Lock()
		if cur, ok := t.states[key]; ok && cur == ls {
			delete(t.states, key)
		}
		t.mu.Unlock()
	}
	return context.WithValue(ctx, loadTrackerKey{}, ls), done
}

func (t *LoadTracker) Status(bookID string, pageNumber int) LoadProgress {
	if t == nil || bookID == "" {
		return LoadProgress{BookID: bookID, Stage: "idle"}
	}
	t.mu.RLock()
	var ls *loadState
	if pageNumber > 0 {
		ls = t.states[trackerKey(bookID, pageNumber)]
	}
	// If page load is waiting on archive index build, surface the active index state too.
	if ls == nil {
		ls = t.states[trackerKey(bookID, 0)]
	}
	t.mu.RUnlock()
	if ls == nil {
		return LoadProgress{BookID: bookID, Stage: "idle"}
	}
	return ls.snapshot()
}

func loadStateFromContext(ctx context.Context) *loadState {
	if ctx == nil {
		return nil
	}
	ls, _ := ctx.Value(loadTrackerKey{}).(*loadState)
	return ls
}

func reportLoadProvider(ctx context.Context, provider string) {
	ls := loadStateFromContext(ctx)
	if ls == nil || provider == "" {
		return
	}
	ls.mu.Lock()
	ls.provider = provider
	ls.updatedAt = time.Now()
	ls.mu.Unlock()
}

func reportLoadStage(ctx context.Context, stage, message string) {
	ls := loadStateFromContext(ctx)
	if ls == nil {
		return
	}
	ls.mu.Lock()
	if stage != "" {
		ls.stage = stage
	}
	if message != "" {
		ls.message = message
	}
	ls.updatedAt = time.Now()
	ls.mu.Unlock()
}

func reportLoadBytes(ctx context.Context, delta, totalHint int64) {
	ls := loadStateFromContext(ctx)
	if ls == nil {
		return
	}
	if delta > 0 {
		ls.bytesLoaded.Add(delta)
	}
	if totalHint > 0 && ls.bytesTotal.Load() <= 0 {
		ls.bytesTotal.Store(totalHint)
	}
	ls.mu.Lock()
	ls.updatedAt = time.Now()
	ls.mu.Unlock()
}

func reportLoadPagesFound(ctx context.Context, count int) {
	ls := loadStateFromContext(ctx)
	if ls == nil || count <= 0 {
		return
	}
	ls.pagesFound.Store(int64(count))
	ls.mu.Lock()
	ls.updatedAt = time.Now()
	ls.mu.Unlock()
}

func readAllReporting(ctx context.Context, r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 64<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			reportLoadBytes(ctx, int64(n), 0)
		}
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			return buf.Bytes(), err
		}
	}
}
