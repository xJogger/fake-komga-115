package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/httpserver"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
)

type Options struct {
	Host    string
	Port    int
	DataDir string
	Logger  *slog.Logger
}

type App struct {
	server  *http.Server
	store   *database.Store
	scanner *scanner.Manager
	covers  *thumbnail.BatchManager
	tasks   *maintenance.Manager
	archive *archive.Service
	logger  *slog.Logger
}

func New(_ context.Context, options Options) (*App, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	dataDir = filepath.Clean(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	store, err := database.Open(filepath.Join(dataDir, "fake-komga-115.db"))
	if err != nil {
		return nil, err
	}
	cacheManager, err := cache.New(store, filepath.Join(dataDir, "cache"))
	if err != nil {
		store.Close()
		return nil, err
	}
	client := oneonefive.New(store, options.Logger)
	scanManager := scanner.New(store, client, options.Logger)
	archiveService := archive.NewService(store, client, cacheManager, options.Logger)
	thumbnailService, err := thumbnail.New(
		store, filepath.Join(dataDir, "thumbnails", "series"), options.Logger,
	)
	if err != nil {
		archiveService.Close()
		scanManager.Close()
		store.Close()
		return nil, err
	}
	coverManager := thumbnail.NewBatchManager(
		store, archiveService, thumbnailService, options.Logger,
	)
	maintenanceManager := maintenance.New(
		store, archiveService, thumbnailService, options.Logger,
	)
	handler := httpserver.New(
		store, client, scanManager, cacheManager, archiveService, thumbnailService,
		coverManager, maintenanceManager, httpserver.RuntimeInfo{
			DataDir: dataDir,
			Host:    options.Host,
			Port:    options.Port,
		}, options.Logger,
	)
	server := &http.Server{
		Addr:              net.JoinHostPort(options.Host, strconv.Itoa(options.Port)),
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return &App{
		server: server, store: store, scanner: scanManager, covers: coverManager,
		tasks: maintenanceManager, archive: archiveService, logger: options.Logger,
	}, nil
}

func (a *App) Run() error {
	errCh, err := a.Start()
	if err != nil {
		return err
	}
	return <-errCh
}

// Start binds the listening socket before returning. Callers can safely open a
// browser after this succeeds without racing a later bind failure.
func (a *App) Start() (<-chan error, error) {
	listener, err := net.Listen("tcp", a.server.Addr)
	if err != nil {
		return nil, err
	}
	a.logger.Info("server listening", "address", listener.Addr().String())
	errCh := make(chan error, 1)
	go func() {
		err := a.server.Serve(listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		errCh <- err
		close(errCh)
	}()
	return errCh, nil
}

func (a *App) Shutdown(ctx context.Context) error { return a.server.Shutdown(ctx) }

func (a *App) Close() error {
	a.tasks.Close()
	a.covers.Close()
	a.scanner.Close()
	a.archive.Close()
	return a.store.Close()
}
