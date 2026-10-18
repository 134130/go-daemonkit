package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerCLIHelper(t *testing.T) {
	if os.Getenv("DAEMONKIT_CLI_HELPER") != "1" {
		return
	}
	if gate := os.Getenv("DAEMONKIT_CLI_GATE"); gate != "" {
		for {
			if _, err := os.Stat(gate); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	m, err := NewManager(Options{AppID: "test", Version: "cli-test"}, "-test.run=^TestManagerDaemonHelper$")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func startTestCLI(t *testing.T, ctx context.Context) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestManagerCLIHelper$")
	command.Env = append(os.Environ(), "DAEMONKIT_CLI_HELPER=1", "GOMAXPROCS=2")
	output := new(bytes.Buffer)
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	return command, output
}

func TestEnsureRunningAcross100CLIProcesses(t *testing.T) {
	m, marker := testManager(t)
	gate := filepath.Join(filepath.Dir(marker), "cli-gate")
	if err := os.MkdirAll(filepath.Dir(gate), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAEMONKIT_CLI_GATE", gate)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	type child struct {
		command *exec.Cmd
		output  *bytes.Buffer
	}
	children := make([]child, 0, 100)
	// Start all callers before releasing the shared barrier.
	for range 100 {
		command, output := startTestCLI(t, ctx)
		children = append(children, child{command, output})
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err := child.command.Wait(); err != nil {
			t.Errorf("CLI: %v: %s", err, child.output)
		}
	}
	assertLaunchCount(t, marker, 1)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRunningAfterLauncherKilled(t *testing.T) {
	m, marker := testManager(t)
	gate := filepath.Join(filepath.Dir(marker), "daemon-gate")
	t.Setenv("DAEMONKIT_TEST_GATE", gate)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command, _ := startTestCLI(t, ctx)
	defer func() { _ = command.Process.Kill() }()
	waitForFile(t, marker)
	var pending launchRecord
	for {
		var err error
		pending, err = readLaunch(m.app.directory)
		if err == nil && pending.PID > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("launcher did not record child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("launcher unexpectedly succeeded")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if record.PID != pending.PID || record.LaunchNonce != pending.LaunchNonce {
		t.Fatal("starting daemon was replaced")
	}
	assertLaunchCount(t, marker, 1)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRunningAfterDaemonCrash(t *testing.T) {
	m, marker := testManager(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := m.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(before.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if alive, err := processMatches(before.PID, before.ProcessIdentity); err != nil || !alive {
		t.Fatalf("fixture exited: %v", err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := waitProcessExit(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if before.LaunchNonce == after.LaunchNonce {
		t.Fatal("crashed generation was reused")
	}
	assertLaunchCount(t, marker, 2)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertLaunchCount(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "\n"); count != want {
		t.Fatalf("spawned %d processes; want %d", count, want)
	}
}
