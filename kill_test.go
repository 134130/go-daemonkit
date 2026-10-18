package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestKillOwnedProcessRejectsReusedPID(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("safe force termination is unavailable on macOS")
	}
	if err := killOwnedProcess(executionRecord{PID: os.Getpid(), ProcessIdentity: "another-process"}); err != nil {
		t.Fatal(err)
	}
}

func TestKillOwnedProcess(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("safe force termination is unavailable on macOS")
	}
	m, _ := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := killOwnedProcess(record); err != nil {
		t.Fatal(err)
	}
	if err := waitProcessExit(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStopForcesExitAfterGrace(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("safe force termination is unavailable on macOS")
	}
	m, marker := testManager(t)
	t.Setenv("DAEMONKIT_TEST_EXIT_GATE", filepath.Join(filepath.Dir(marker), "never-exit"))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := m.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < stopGrace {
		t.Fatal("force termination did not respect shutdown grace")
	}
	if alive, err := processMatches(record.PID, record.ProcessIdentity); err != nil || alive {
		t.Fatalf("process survived forced stop: %v", err)
	}
}
