package cache

import (
	"context"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestEnforceLimit(t *testing.T) {
	store, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := New(store, t.TempDir()+"/cache")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.Put(ctx, TypeRange, "first", make([]byte, 60), 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.Put(ctx, TypeRange, "second", make([]byte, 60), 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.EnforceLimit(ctx, TypeRange, 100); err != nil {
		t.Fatal(err)
	}
	stats, err := manager.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].Files != 1 || stats[0].Bytes != 60 {
		t.Fatalf("unexpected limited cache stats: %+v", stats[0])
	}
	if err := manager.EnforceLimit(ctx, TypeRange, 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.Put(ctx, TypeRange, "third", make([]byte, 60), 0); err != nil {
		t.Fatal(err)
	}
	stats, err = manager.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].Files != 2 || stats[0].Bytes != 120 {
		t.Fatalf("zero limit should be unlimited: %+v", stats[0])
	}
}
