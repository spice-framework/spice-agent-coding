package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestRootOwnsCancellationAndRequiresContainmentRegistry(t *testing.T) {
	t.Parallel()
	registry := &rootRegistryFixture{}
	root, cleanup, err := NewRoot(registry)
	if err != nil {
		t.Fatalf("NewRoot() error = %v", err)
	}
	if _, err = rootContext(root); err != nil {
		t.Fatalf("rootContext() error = %v", err)
	}
	if err = cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if !errors.Is(root.Err(), context.Canceled) {
		t.Fatalf("root error = %v, want cancellation", root.Err())
	}
	if _, err = rootContext(root); err == nil {
		t.Fatal("rootContext() accepted a canceled root")
	}
	if _, _, err = NewRoot(nil); err == nil {
		t.Fatal("NewRoot() accepted a nil registry")
	}
}

func TestRootRegistryAdoptionReturnsGeneratedCleanup(t *testing.T) {
	registry, cleanup, err := NewRootRegistry()
	if err != nil {
		t.Fatalf("NewRootRegistry() error = %v", err)
	}
	if registry == nil || cleanup == nil {
		t.Fatal("NewRootRegistry() returned incomplete ownership")
	}
	if err = cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if err = cleanup(context.Background()); err != nil {
		t.Fatalf("idempotent cleanup() error = %v", err)
	}
}

type rootRegistryFixture struct{}

func (*rootRegistryFixture) Close() error { return nil }
