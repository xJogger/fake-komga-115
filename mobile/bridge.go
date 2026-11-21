//go:build android

// Package mobile is the gomobile entry point for the standalone Android app.
package mobile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/app"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
	"github.com/xJogger/fake-komga-115/internal/localfs"
)

type DocumentBackend interface {
	List(root, rel string) (string, error)
	Open(root, rel string) (int32, error)
}

type Instance struct {
	mu      sync.Mutex
	app     *app.App
	dataDir string
	url     string
	token   string
}

var embedded sync.Mutex

func Start(dataDir, cacheDir string, backend DocumentBackend, preferredPort int32) (*Instance, error) {
	if backend == nil || !embedded.TryLock() {
		return nil, errors.New("embedded server cannot be started")
	}
	localfs.SetDocumentBackend(backend)
	ready := false
	defer func() {
		if !ready {
			localfs.SetDocumentBackend(nil)
			embedded.Unlock()
		}
	}()
	if err := prepareSQLiteTemp(cacheDir); err != nil {
		diagnostics.WriteStartupFailure(dataDir, "mobile", "startup", err)
		return nil, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		diagnostics.WriteStartupFailure(dataDir, "mobile", "startup", err)
		return nil, err
	}
	token := hex.EncodeToString(nonce[:])
	server, err := app.New(context.Background(), app.Options{Host: "127.0.0.1", Port: int(preferredPort), DataDir: dataDir, LoopbackToken: token})
	if err != nil {
		diagnostics.WriteStartupFailure(dataDir, "mobile", "startup", err)
		return nil, err
	}
	if _, err := server.Start(); err != nil {
		diagnostics.WriteStartupFailure(dataDir, "mobile", "startup", err)
		server.Close()
		return nil, err
	}
	ready = true
	return &Instance{app: server, dataDir: dataDir, url: "http://" + server.ListenAddr(), token: token}, nil
}

func (i *Instance) Origin() string { return i.url }
func (i *Instance) Token() string  { return i.token }

func (i *Instance) HasAdmin() bool {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return false
	}
	ok, err := srv.HasAdmin(context.Background())
	return err == nil && ok
}

func (i *Instance) EnableLan(port int32) error {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return errors.New("embedded server is not running")
	}
	_, err := srv.StartLANServer(int(port))
	return err
}

func (i *Instance) DisableLan() error {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.StopLANServer()
}

func (i *Instance) LanActive() bool {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return false
	}
	active, _, _ := srv.LANStatus()
	return active
}

func (i *Instance) LanPort() int32 {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return 0
	}
	_, port, _ := srv.LANStatus()
	return int32(port)
}

func (i *Instance) LanError() string {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return ""
	}
	_, _, msg := srv.LANStatus()
	return msg
}

func (i *Instance) DiagnosticsVerbose() bool {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil || srv.Diagnostics() == nil {
		return false
	}
	return srv.Diagnostics().Verbose()
}

func (i *Instance) SetDiagnosticsVerbose(verbose bool) error {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil {
		return errors.New("embedded server is not running")
	}
	return srv.SetDiagnosticsVerbose(context.Background(), verbose)
}

func (i *Instance) RecordDiagnostic(level, stage, action, code, detail string) {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv == nil || srv.Diagnostics() == nil {
		return
	}
	srv.Diagnostics().Record(diagnostics.Event{
		Level:     level,
		Component: "android",
		Stage:     stage,
		Action:    action,
		Code:      code,
		Detail:    detail,
	})
}

func (i *Instance) ExportDiagnostics() string {
	i.mu.Lock()
	srv := i.app
	dataDir := i.dataDir
	i.mu.Unlock()
	if srv != nil && srv.Diagnostics() != nil {
		return srv.Diagnostics().ExportText(500)
	}
	return ExportDiagnosticsFromDir(dataDir)
}

func (i *Instance) ClearDiagnostics() error {
	i.mu.Lock()
	srv := i.app
	i.mu.Unlock()
	if srv != nil && srv.Diagnostics() != nil {
		return srv.Diagnostics().Clear()
	}
	return nil
}

// ExportDiagnosticsFromDir lets Android export sanitized logs even if the Go server failed to start.
func ExportDiagnosticsFromDir(dataDir string) string {
	rec, err := diagnostics.New(dataDir, false)
	if err != nil {
		return "# MangaAlpha Diagnostic Report\n(unable to open diagnostics directory)\n"
	}
	return rec.ExportText(500)
}

func (i *Instance) Stop() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.app == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	_ = i.app.Shutdown(ctx)
	cancel()
	_ = i.app.Close()
	i.app = nil
	localfs.SetDocumentBackend(nil)
	embedded.Unlock()
}
