package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	app := testApp(t)
	marker := filepath.Join(filepath.Dir(app.directory), "launches.txt")
	t.Setenv("DAEMONKIT_MANAGER_HELPER", "1")
	t.Setenv("DAEMONKIT_TEST_LAUNCHES", marker)
	manager, err := NewManager(app.options, "-test.run=^TestManagerDaemonHelper$")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		record, err := readExecution(app.directory)
		pid, identity := record.PID, record.ProcessIdentity
		if err != nil {
			pending, err := readLaunch(app.directory)
			if err != nil {
				return
			}
			pid, identity = pending.PID, pending.ProcessIdentity
		}
		if pid == 0 {
			return
		}
		if alive, err := processMatches(pid, identity); err == nil && alive {
			process, err := os.FindProcess(pid)
			if err != nil {
				t.Error(err)
				return
			}
			_ = process.Kill()
			_ = process.Release()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if alive, _ := processMatches(pid, identity); !alive {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Error("test daemon was not reaped")
		}
	})
	return manager, marker
}

func TestManagerDaemonHelper(t *testing.T) {
	if os.Getenv("DAEMONKIT_MANAGER_HELPER") != "1" {
		return
	}
	marker := os.Getenv("DAEMONKIT_TEST_LAUNCHES")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintln(file, executable)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DAEMONKIT_TEST_EXIT") == "1" {
		return
	}
	if gate := os.Getenv("DAEMONKIT_TEST_GATE"); gate != "" {
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	app, err := New(Options{AppID: "test", Version: "managed-child"})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DAEMONKIT_TEST_LOGS") == "1" {
		if _, err := fmt.Fprintln(os.Stdout, "raw stdout"); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stderr, "raw stderr"); err != nil {
			t.Fatal(err)
		}
		if err := app.Register(&logLifecycleService{writer: app.LogWriter()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if gate := os.Getenv("DAEMONKIT_TEST_EXIT_GATE"); gate != "" {
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestEnsureRunningConcurrent(t *testing.T) {
	manager, marker := testManager(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	errorsCh := make(chan error, 100)
	var callers sync.WaitGroup
	for range 100 {
		callers.Go(func() { errorsCh <- manager.EnsureRunning(ctx) })
	}
	callers.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("spawned %d processes", len(lines))
	}
	if !strings.HasPrefix(lines[0], filepath.Join(manager.app.directory, "releases")+string(os.PathSeparator)) {
		t.Fatalf("daemon ran outside managed release: %q", lines[0])
	}
	if _, err := readLaunch(manager.app.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launch reservation retained after Ready: %v", err)
	}
}

func TestEnsureRunningCancellationPreservesStartingDaemon(t *testing.T) {
	manager, marker := testManager(t)
	gate := filepath.Join(filepath.Dir(marker), "ready-gate")
	t.Setenv("DAEMONKIT_TEST_GATE", gate)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- manager.EnsureRunning(ctx) }()
	waitForFile(t, marker)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller = %v", err)
	}
	pending, err := readLaunch(manager.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if alive, err := processMatches(pending.PID, pending.ProcessIdentity); err != nil || !alive {
		t.Fatalf("starting daemon did not survive: %v", err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if err := manager.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := readExecution(manager.app.directory)
	if err != nil || record.PID != pending.PID || record.LaunchNonce != pending.LaunchNonce {
		t.Fatalf("launch generation changed: %+v, %v", record, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("duplicate spawn: %q, %v", data, err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not start")
}

func TestEnsureRunningReportsEarlyExit(t *testing.T) {
	manager, _ := testManager(t)
	t.Setenv("DAEMONKIT_TEST_EXIT", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := manager.EnsureRunning(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup failure = %v", err)
	}
	if _, err := readLaunch(manager.app.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead child reservation retained: %v", err)
	}
}

func TestEnsureRunningDoesNotGuessUnknownLaunch(t *testing.T) {
	manager, marker := testManager(t)
	unlock, err := lockOperation(t.Context(), manager.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	err = writeJSONRecord(manager.app.directory, "launch.json", launchRecord{LaunchNonce: rand.Text()})
	_ = unlock()
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if err := manager.EnsureRunning(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ambiguous launch = %v", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("spawned despite ambiguous launch: %v", err)
		}
	})
}

func TestManagerReusesForegroundDaemon(t *testing.T) {
	manager, marker := testManager(t)
	_, _ = runTestApp(t, manager.app)
	conn, before := connectTestApp(t, manager.app)
	if err := manager.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("spawned despite existing foreground daemon")
	}
	_, after := connectTestApp(t, manager.app)
	if before.LaunchNonce != after.LaunchNonce {
		t.Fatal("existing generation was replaced")
	}
	if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`9`), Method: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readRPC(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
}
