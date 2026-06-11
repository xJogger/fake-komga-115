package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/httpserver"
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
	logger  *slog.Logger
}

func New(_ context.Context, options Options) (*App, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if err := os.MkdirAll(options.DataDir, 0o700); err != nil {
		return nil, err
	}
	store, err := database.Open(filepath.Join(options.DataDir, "fake-komga-115.db"))
	if err != nil {
		return nil, err
	}
	cacheManager, err := cache.New(store, filepath.Join(options.DataDir, "cache"))
	if err != nil {
		store.Close()
		return nil, err
	}
	client := oneonefive.New(store, options.Logger)
	scanManager := scanner.New(store, client, options.Logger)
	archiveService := archive.NewService(store, client, cacheManager, options.Logger)
	thumbnailService, err := thumbnail.New(
		store, filepath.Join(options.DataDir, "thumbnails", "series"), options.Logger,
	)
	if err != nil {
		scanManager.Close()
		store.Close()
		return nil, err
	}
	handler := httpserver.New(
		store, client, scanManager, cacheManager, archiveService, thumbnailService, options.Logger,
	)
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", options.Host, options.Port),
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	return &App{server: server, store: store, scanner: scanManager, logger: options.Logger}, nil
}

func (a *App) Run() error {
	a.logger.Info("server listening", "address", a.server.Addr)
	err := a.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (a *App) Shutdown(ctx context.Context) error { return a.server.Shutdown(ctx) }

func (a *App) Close() error {
	a.scanner.Close()
	return a.store.Close()
}
