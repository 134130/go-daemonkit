package daemon

import (
	"os"
	"runtime"
	"testing"
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
