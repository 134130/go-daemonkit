package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestManagerStopRestart(t *testing.T) {
	m, _ := testManager(t)
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if before.LaunchNonce == after.LaunchNonce {
		t.Fatal("restart reused execution generation")
	}
	if alive, err := processMatches(before.PID, before.ProcessIdentity); err != nil || alive {
		t.Fatalf("old process still alive: %v", err)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if alive, err := processMatches(after.PID, after.ProcessIdentity); err != nil || alive {
		t.Fatalf("Stop returned before process exit: %v", err)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStopWaitsBeyondShutdownAcknowledgement(t *testing.T) {
	m, marker := testManager(t)
	gate := filepath.Join(filepath.Dir(marker), "exit-gate")
	t.Setenv("DAEMONKIT_TEST_EXIT_GATE", gate)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Stop(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(m.app.directory, "runtime.json")); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown was not accepted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("returned before exit: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if alive, err := processMatches(record.PID, record.ProcessIdentity); err != nil || !alive {
		t.Fatalf("caller cancellation killed daemon: %v", err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exitCtx, end := context.WithTimeout(t.Context(), 5*time.Second)
	defer end()
	if err := m.Stop(exitCtx); err != nil {
		t.Fatal(err)
	}
	if err := waitProcessExit(exitCtx, record); err != nil {
		t.Fatal(err)
	}
}

func TestWaitProcessExitDeadline(t *testing.T) {
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := waitProcessExit(ctx, executionRecord{PID: os.Getpid(), ProcessIdentity: identity}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait = %v", err)
		}
	})
}

func TestStopRejectsWrongGeneration(t *testing.T) {
	m, _ := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.LaunchNonce = "different-generation"
	if err := writeExecution(m.app.directory, changed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writeExecution(m.app.directory, record) })
	if err := m.Stop(t.Context()); err == nil {
		t.Fatal("accepted mismatched generation")
	}
	if alive, err := processMatches(record.PID, record.ProcessIdentity); err != nil || !alive {
		t.Fatalf("wrong generation was stopped: %v", err)
	}
}

func TestManagerConcurrentLifecycle(t *testing.T) {
	m, _ := testManager(t)
	results := make(chan error, 12)
	for i := range 12 {
		go func() {
			switch i % 3 {
			case 0:
				results <- m.EnsureRunning(t.Context())
			case 1:
				results <- m.Stop(t.Context())
			case 2:
				results <- m.Restart(t.Context())
			}
		}()
	}
	for range 12 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
