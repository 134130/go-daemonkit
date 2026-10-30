package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusStoppedDoesNotCreateFiles(t *testing.T) {
	app := testApp(t)
	manager := &Manager{app: app}
	status, err := manager.Status(t.Context())
	if err != nil || status.State != StatusStopped || status.PID != 0 {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	if status.AppID != "test" || status.InstanceID != "default" || status.StateDir != app.directory || status.Endpoint != app.endpoint || status.LogPath != filepath.Join(app.directory, "logs", "daemon.log") || status.StdoutPath != filepath.Join(app.directory, "logs", "daemon.stdout") || status.StderrPath != filepath.Join(app.directory, "logs", "daemon.stderr") {
		t.Fatalf("paths: %+v", status)
	}
	if _, err := os.Stat(app.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Status created resources: %v", err)
	}
}

func TestStatusReadyUsesDaemonVersion(t *testing.T) {
	app := testApp(t)
	cancel, done := runTestApp(t, app)
	_, _ = connectTestApp(t, app)
	caller, err := New(Options{AppID: "test", Version: "different-client"})
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{app: caller}
	record, err := readExecution(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	// Status must not clear a reservation as EnsureRunning's probe does.
	pending := launchRecord{PID: record.PID, ProcessIdentity: record.ProcessIdentity, LaunchNonce: record.LaunchNonce}
	if err := writeJSONRecord(app.directory, "launch.json", pending); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(t.Context())
	if err != nil || status.State != StatusReady || status.Version != "test-binary" || status.ClientVersion != "different-client" || status.ProtocolVersion != protocolVersion || status.PID != os.Getpid() {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	changed := record
	changed.LaunchNonce = "different-generation"
	if err := writeExecution(app.directory, changed); err != nil {
		t.Fatal(err)
	}
	mismatch, mismatchErr := manager.Status(t.Context())
	if err := writeExecution(app.directory, record); err != nil {
		t.Fatal(err)
	}
	if mismatchErr == nil || mismatch.State == StatusReady || mismatch.Version != "" {
		t.Fatalf("mismatched generation: %+v %v", mismatch, mismatchErr)
	}
	if _, err := readLaunch(app.directory); err != nil {
		t.Fatalf("reservation removed: %v", err)
	}
	cancel()
	_ = awaitApp(t, done)
	// Remove our artificial reservation; its PID is the still-running test process.
	if err := os.Remove(filepath.Join(app.directory, "launch.json")); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(t.Context())
	if err != nil || status.State != StatusStopped {
		t.Fatalf("stopped: %+v %v", status, err)
	}
}

func TestStatusUnavailableAndUnknown(t *testing.T) {
	app := testApp(t)
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	manager := &Manager{app: app}
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: identity, LaunchNonce: "test-status-generation"}
	if err := writeExecution(app.directory, record); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(t.Context())
	if err == nil || status.State != StatusRunning || status.PID != os.Getpid() || status.Version != "" {
		t.Fatalf("unavailable: %+v %v", status, err)
	}
}

func TestStatusStaleRecordIsNotLiveOrRemoved(t *testing.T) {
	app := testApp(t)
	if err := os.MkdirAll(app.directory, 0700); err != nil {
		t.Fatal(err)
	}
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: "reused-pid", LaunchNonce: "old-generation"}
	if err := writeExecution(app.directory, record); err != nil {
		t.Fatal(err)
	}
	status, err := (&Manager{app: app}).Status(t.Context())
	if err != nil || status.State != StatusStopped || status.PID != 0 {
		t.Fatalf("stale: %+v %v", status, err)
	}
	if got, err := readExecution(app.directory); err != nil || got != record {
		t.Fatalf("record modified: %+v %v", got, err)
	}
}

func TestStatusUnknownLaunchAndCorruptRecord(t *testing.T) {
	app := testApp(t)
	if err := os.MkdirAll(app.directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONRecord(app.directory, "launch.json", launchRecord{LaunchNonce: "unknown-launch-generation"}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{app: app}
	status, err := manager.Status(t.Context())
	if err == nil || status.State != StatusUnknown {
		t.Fatalf("unknown launch: %+v %v", status, err)
	}
	if err := os.WriteFile(filepath.Join(app.directory, "runtime.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(t.Context())
	if err == nil || status.State != StatusUnknown || status.LogPath == "" {
		t.Fatalf("corrupt: %+v %v", status, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	status, err = manager.Status(ctx)
	if !errors.Is(err, context.Canceled) || status.State != StatusUnknown || status.StateDir == "" {
		t.Fatalf("canceled: %+v %v", status, err)
	}
}
