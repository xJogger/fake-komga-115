package archive

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	defaultVolumePrefetchRemainingPages = 10
	maxVolumePrefetchRemainingPages     = 100
)

type volumeIndexBuilder func(context.Context, database.Book) ([]PageEntry, error)

type volumePrefetchJob struct {
	key        string
	book       database.Book
	pageNumber int
	totalPages int
}

// VolumeIndexPrefetcher builds only the next Book's archive index. It has one
// worker so remote index reads are globally serialized, and pending contains
// both queued and active jobs so repeated near-end page requests are merged.
type VolumeIndexPrefetcher struct {
	store      *database.Store
	buildIndex volumeIndexBuilder
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan volumePrefetchJob
	wg     sync.WaitGroup

	mu      sync.Mutex
	pending map[string]struct{}
}

func newVolumeIndexPrefetcher(
	store *database.Store,
	buildIndex volumeIndexBuilder,
	logger *slog.Logger,
) *VolumeIndexPrefetcher {
	ctx, cancel := context.WithCancel(context.Background())
	prefetcher := &VolumeIndexPrefetcher{
		store: store, buildIndex: buildIndex, logger: logger,
		ctx: ctx, cancel: cancel, jobs: make(chan volumePrefetchJob, 64),
		pending: make(map[string]struct{}),
	}
	prefetcher.wg.Add(1)
	go prefetcher.worker()
	return prefetcher
}

func (p *VolumeIndexPrefetcher) Trigger(
	book database.Book,
	pageNumber, totalPages int,
) {
	if pageNumber < 1 || totalPages < 1 || pageNumber > totalPages {
		return
	}
	key := book.ID + "\x00" + BookVersion(book)
	p.mu.Lock()
	if _, exists := p.pending[key]; exists {
		p.mu.Unlock()
		return
	}
	p.pending[key] = struct{}{}
	p.mu.Unlock()

	job := volumePrefetchJob{
		key: key, book: book, pageNumber: pageNumber, totalPages: totalPages,
	}
	select {
	case p.jobs <- job:
	case <-p.ctx.Done():
		p.finish(key)
	default:
		p.finish(key)
		p.logger.Warn("next volume index prefetch queue is full", "book", book.ID)
	}
}

func (p *VolumeIndexPrefetcher) Close() {
	p.cancel()
	p.wg.Wait()
}

func (p *VolumeIndexPrefetcher) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job := <-p.jobs:
			p.process(job)
			p.finish(job.key)
		}
	}
}

func (p *VolumeIndexPrefetcher) process(job volumePrefetchJob) {
	if !p.store.BoolSetting(p.ctx, "volume_index_prefetch_enabled", false) {
		return
	}
	remaining := p.store.Int64Setting(
		p.ctx, "volume_index_prefetch_remaining_pages",
		defaultVolumePrefetchRemainingPages,
	)
	if remaining < 1 || remaining > maxVolumePrefetchRemainingPages {
		remaining = defaultVolumePrefetchRemainingPages
	}
	if job.totalPages-job.pageNumber > int(remaining) {
		return
	}

	next, err := p.store.NextBookInSeries(p.ctx, job.book)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		p.logger.Warn(
			"next volume index prefetch could not resolve the next book",
			"book", job.book.ID,
		)
		return
	}

	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	defer cancel()
	pages, err := p.buildIndex(ctx, next)
	if err != nil {
		p.logger.Warn(
			"next volume index prefetch failed",
			"book", next.ID,
			"reason", volumePrefetchErrorReason(err),
		)
		return
	}
	p.logger.Info(
		"next volume index prefetched",
		"current_book", job.book.ID,
		"next_book", next.ID,
		"pages", len(pages),
	)
}

func (p *VolumeIndexPrefetcher) finish(key string) {
	p.mu.Lock()
	delete(p.pending, key)
	p.mu.Unlock()
}

func volumePrefetchErrorReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrRangeNotSupported):
		return "range_not_supported"
	case errors.Is(err, ErrUnsupportedArchive):
		return "unsupported_archive"
	case errors.Is(err, ErrInvalidZIP), errors.Is(err, ErrInvalidRAR):
		return "invalid_archive"
	case errors.Is(err, ErrUnsupportedZIP), errors.Is(err, ErrUnsupportedRAR):
		return "unsupported_archive_feature"
	case errors.Is(err, ErrSolidRAR):
		return "solid_rar"
	case errors.Is(err, ErrEncryptedRAR):
		return "encrypted_archive"
	case errors.Is(err, ErrMultiVolumeRAR):
		return "multi_volume_rar"
	default:
		return "index_build_failed"
	}
}
