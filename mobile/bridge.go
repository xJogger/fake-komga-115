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
	"github.com/xJogger/fake-komga-115/internal/localfs"
)

type DocumentBackend interface {
	List(root, rel string) (string, error)
	Open(root, rel string) (int32, error)
}

type Instance struct {
	mu    sync.Mutex
	app   *app.App
	url   string
	token string
}

var embedded sync.Mutex

func Start(dataDir string, backend DocumentBackend, preferredPort int32) (*Instance, error) {
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
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(nonce[:])
	server, err := app.New(context.Background(), app.Options{Host: "127.0.0.1", Port: int(preferredPort), DataDir: dataDir, LoopbackToken: token})
	if err != nil {
		return nil, err
	}
	if _, err := server.Start(); err != nil {
		server.Close()
		return nil, err
	}
	ready = true
	return &Instance{app: server, url: "http://" + server.ListenAddr(), token: token}, nil
}

func (i *Instance) Origin() string { return i.url }
func (i *Instance) Token() string  { return i.token }
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
