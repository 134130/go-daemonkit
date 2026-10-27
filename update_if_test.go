package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	if !((a == nil && errors.Is(b, ErrUpdateConflict)) || (b == nil && errors.Is(a, ErrUpdateConflict))) {
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
