package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xJogger/fake-komga-115/internal/app"
	"github.com/xJogger/fake-komga-115/internal/buildinfo"
)

func main() {
	defaultDir := defaultDataDir(runtime.GOOS, os.Getenv, os.UserConfigDir)
	var (
		host        = flag.String("host", envOr("FK115_HOST", "0.0.0.0"), "listen host")
		port        = flag.Int("port", envInt("FK115_PORT", 25600), "listen port")
		dataDir     = flag.String("data-dir", envOr("FK115_DATA_DIR", defaultDir), "data directory")
		openBrowser = flag.Bool(
			"open-browser",
			envBool("FK115_OPEN_BROWSER", runtime.GOOS == "windows"),
			"open the admin page after the server starts",
		)
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Options{
		Host:    *host,
		Port:    *port,
		DataDir: *dataDir,
		Logger:  logger,
	})
	if err != nil {
		logger.Error("initialize application", "error", err)
		os.Exit(1)
	}
	defer a.Close()

	errCh, err := a.Start()
	if err != nil {
		logger.Error("start server", "error", err)
		_ = a.Close()
		os.Exit(1)
	}
	if *openBrowser {
		adminURL := localAdminURL(*host, *port)
		go func() {
			time.Sleep(250 * time.Millisecond)
			if err := openURL(runtime.GOOS, adminURL); err != nil {
				logger.Warn("open browser", "error", err, "url", adminURL)
			}
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "error", err)
		}
	case err := <-errCh:
		if err != nil {
			logger.Error("server stopped", "error", err)
			_ = a.Close()
			os.Exit(1)
		}
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	var value int
	if _, err := fmt.Sscanf(os.Getenv(key), "%d", &value); err == nil && value > 0 {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func defaultDataDir(
	goos string,
	getenv func(string) string,
	userConfigDir func() (string, error),
) string {
	if goos != "windows" {
		return "./data"
	}
	if base := strings.TrimSpace(getenv("LOCALAPPDATA")); base != "" {
		return filepath.Join(base, "fake-komga-115", "data")
	}
	if base, err := userConfigDir(); err == nil && strings.TrimSpace(base) != "" {
		return filepath.Join(base, "fake-komga-115", "data")
	}
	return filepath.Join(".", "data")
}

func localAdminURL(host string, port int) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/admin"
}

func openURL(goos, url string) error {
	name, args, err := browserCommand(goos, url)
	if err != nil {
		return err
	}
	command := exec.Command(name, args...)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func browserCommand(goos, url string) (string, []string, error) {
	switch goos {
	case "windows":
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", url}, nil
	case "darwin":
		return "open", []string{url}, nil
	case "linux":
		return "xdg-open", []string{url}, nil
	default:
		return "", nil, errors.New("opening a browser is not supported on " + goos)
	}
}
