package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

type RemoteReaderAt struct {
	ctx    context.Context
	book   database.Book
	store  *database.Store
	client *oneonefive.Client
	cache  *cache.Manager
	http   *http.Client
	logger *slog.Logger

	blockSize int64
	maxBytes  int64
	ua        string

	urlMu sync.Mutex
}

func NewRemoteReaderAt(
	ctx context.Context,
	book database.Book,
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
) *RemoteReaderAt {
	return NewRemoteReaderAtWithBlockSize(
		ctx, book, store, client, cacheManager, logger,
		store.Int64Setting(ctx, "range_block_size", 1<<20),
	)
}

func NewRemoteReaderAtWithBlockSize(
	ctx context.Context,
	book database.Book,
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
	blockSize int64,
) *RemoteReaderAt {
	if blockSize <= 0 {
		blockSize = 1 << 20
	}
	return &RemoteReaderAt{
		ctx:       ctx,
		book:      book,
		store:     store,
		client:    client,
		cache:     cacheManager,
		http:      &http.Client{Timeout: 90 * time.Second},
		logger:    logger,
		blockSize: blockSize,
		maxBytes:  store.Int64Setting(ctx, "range_cache_max_bytes", 10<<30),
		ua:        oneonefive.UserAgent,
	}
}

func (r *RemoteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= r.book.Size {
		return 0, io.EOF
	}
	requested := len(p)
	short := false
	if int64(len(p)) > r.book.Size-off {
		p = p[:r.book.Size-off]
		short = true
	}
	if r.blockSize <= 0 {
		r.blockSize = 1 << 20
	}
	written := 0
	for written < len(p) {
		absolute := off + int64(written)
		blockIndex := absolute / r.blockSize
		blockOffset := absolute % r.blockSize
		block, _, err := r.cache.GetOrLoad(
			r.ctx,
			cache.TypeRange,
			r.blockKey(blockIndex),
			r.maxBytes,
			func(ctx context.Context) ([]byte, error) { return r.fetchBlock(ctx, blockIndex) },
		)
		if err != nil {
			return written, err
		}
		if blockOffset >= int64(len(block)) {
			return written, io.ErrUnexpectedEOF
		}
		n := copy(p[written:], block[blockOffset:])
		written += n
	}
	if written < len(p) || short || written < requested {
		return written, io.EOF
	}
	return written, nil
}

func (r *RemoteReaderAt) blockKey(index int64) string {
	return fmt.Sprintf(
		"%s:%d:%s:%d:%d",
		r.book.FileID, r.book.Size, r.book.SHA1, r.blockSize, index,
	)
}

func (r *RemoteReaderAt) fetchBlock(ctx context.Context, index int64) ([]byte, error) {
	start := index * r.blockSize
	end := min(start+r.blockSize-1, r.book.Size-1)
	expected := end - start + 1
	for attempt := 0; attempt < 2; attempt++ {
		download, err := r.downloadURL(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", download.UserAgent)
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		request.Header.Set("Accept-Encoding", "identity")
		response, err := r.http.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusOK {
			response.Body.Close()
			return nil, ErrRangeNotSupported
		}
		if response.StatusCode == http.StatusUnauthorized ||
			response.StatusCode == http.StatusForbidden ||
			response.StatusCode == http.StatusNotFound ||
			response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			response.Body.Close()
			r.invalidateDownloadURL(ctx)
			continue
		}
		if response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			return nil, fmt.Errorf("range request returned HTTP %d", response.StatusCode)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, expected+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(data)) != expected {
			return nil, fmt.Errorf("range request returned %d bytes, expected %d", len(data), expected)
		}
		r.logger.Debug("range read", "file", r.book.FileID, "offset", start, "length", expected)
		return data, nil
	}
	return nil, errors.New("download URL remained invalid after refresh")
}

func (r *RemoteReaderAt) downloadURL(ctx context.Context, force bool) (oneonefive.Download, error) {
	r.urlMu.Lock()
	defer r.urlMu.Unlock()
	key, uaHash := r.downloadCacheKey()
	if !force {
		var item oneonefive.Download
		var expireText string
		err := r.store.DB().QueryRowContext(ctx, `
SELECT url,user_agent,expire_at FROM downurl_cache WHERE cache_key=?`, key).
			Scan(&item.URL, &item.UserAgent, &expireText)
		if err == nil {
			expire, parseErr := time.Parse(time.RFC3339Nano, expireText)
			if parseErr == nil && time.Until(expire) > 30*time.Second {
				return item, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return oneonefive.Download{}, err
		}
	}
	item, err := r.client.DownloadURL(ctx, r.book.FileID, r.book.PickCode, r.ua)
	if err != nil {
		return oneonefive.Download{}, err
	}
	ttl := time.Duration(r.store.Int64Setting(ctx, "downurl_ttl_seconds", 1200)) * time.Second
	if ttl < time.Minute {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	_, err = r.store.DB().ExecContext(ctx, `
INSERT INTO downurl_cache(cache_key,pick_code,ua_hash,url,user_agent,expire_at,created_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(cache_key) DO UPDATE SET
 url=excluded.url,user_agent=excluded.user_agent,expire_at=excluded.expire_at,created_at=excluded.created_at`,
		key, r.book.PickCode, uaHash, item.URL, item.UserAgent,
		now.Add(ttl).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return item, err
}

func (r *RemoteReaderAt) invalidateDownloadURL(ctx context.Context) {
	key, _ := r.downloadCacheKey()
	_, _ = r.store.DB().ExecContext(ctx, `DELETE FROM downurl_cache WHERE cache_key=?`, key)
}

func (r *RemoteReaderAt) downloadCacheKey() (string, string) {
	sum := sha256.Sum256([]byte(r.ua))
	uaHash := hex.EncodeToString(sum[:])
	return r.book.PickCode + ":" + uaHash, uaHash
}

func ParseContentRangeSize(value string) (int64, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, errors.New("invalid Content-Range")
	}
	return strconv.ParseInt(parts[1], 10, 64)
}
