package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
)

func TestUpdateIfGenerationAndJournals(t *testing.T) {
	m, _ := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	for _, name := range []string{"update.json", "stopping.json", "launch.json"} {
		path := filepath.Join(m.app.directory, name)
		if err := os.WriteFile(path, []byte("unreadable journal"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := m.UpdateIf(t.Context(), next.executable, state.UpdateToken); !errors.Is(err, ErrUpdateConflict) {
			t.Fatalf("%s: %v", name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "unreadable journal" {
			t.Fatalf("journal changed: %q, %v", data, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- m.UpdateIf(t.Context(), next.executable, state.UpdateToken) }()
	}
	a, b := <-results, <-results
	oneSucceeded := (a == nil && errors.Is(b, ErrUpdateConflict)) || (b == nil && errors.Is(a, ErrUpdateConflict))
	if !oneSucceeded {
		t.Fatalf("concurrent updates: %v, %v", a, b)
	}
	state, err = m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateIf(t.Context(), m.executable, state.UpdateToken); !errors.Is(err, ErrUpdateConflict) {
		t.Fatalf("restart: %v", err)
	}
	state, err = m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateIf(t.Context(), m.executable, state.UpdateToken); !errors.Is(err, ErrUpdateConflict) {
		t.Fatalf("stop: %v", err)
	}
	if err := m.UpdateIf(t.Context(), m.executable, UpdateToken{}); !errors.Is(err, ErrUpdateConflict) {
		t.Fatalf("zero token: %v", err)
	}
}

func TestUpdateIfCanceledWhileWaitingForOperation(t *testing.T) {
	m, _ := testManager(t)
	unlock, err := lockOperation(t.Context(), m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlock(); err != nil {
			t.Error(err)
		}
	}()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- m.UpdateIf(ctx, m.executable, UpdateToken{}) }()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting cancellation: %v", err)
		}
	})
	for _, name := range []string{"current.json", "update.json", "launch.json"} {
		if _, err := os.Stat(filepath.Join(m.app.directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected %s: %v", name, err)
		}
	}
}
