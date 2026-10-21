package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func updateCandidate(t *testing.T, m *Manager) *Manager {
	t.Helper()
	data, err := os.ReadFile(m.executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "candidate")
	// The executable image is unchanged; trailing data gives this release new content.
	if err = os.WriteFile(path, append(data, []byte("daemonkit-update-test")...), 0700); err != nil {
		t.Fatal(err)
	}
	next := *m
	next.executable = path
	return &next
}

func TestUpdateStoppedAndSelectedInvocation(t *testing.T) {
	m, marker := testManager(t)
	if err := m.Update(t.Context(), m.executable); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline update started a daemon")
	}
	// The next caller's invocation must not replace the selected invocation.
	m.args = []string{"-test.run=^TestUpdateExitHelper$"}
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 1)
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRunning(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Update(t.Context(), m.executable); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 1)
	next := updateCandidate(t, m)
	if err = m.Update(t.Context(), next.executable); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(next.executable); err != nil {
		t.Fatal(err)
	}
	selected, err := m.readSelection()
	if err != nil {
		t.Fatal(err)
	}
	after, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if after.Release != selected.Release || after.LaunchNonce == before.LaunchNonce || after.Release == before.Release {
		t.Fatalf("invalid replacement: %+v -> %+v", before, after)
	}
	assertLaunchCount(t, marker, 2)
	if err = m.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted, err := readExecution(m.app.directory)
	if err != nil || restarted.Release != after.Release {
		t.Fatalf("old CLI changed selected release: %+v, %v", restarted, err)
	}
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRollback(t *testing.T) {
	for _, kind := range []string{"exec-failure", "exit-before-ready"} {
		t.Run(kind, func(t *testing.T) {
			m, marker := testManager(t)
			if err := m.EnsureRunning(t.Context()); err != nil {
				t.Fatal(err)
			}
			before, err := m.readSelection()
			if err != nil {
				t.Fatal(err)
			}
			next := updateCandidate(t, m)
			if kind == "exec-failure" {
				if err = os.WriteFile(next.executable, []byte("not an executable"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				next.args = []string{"-test.run=^TestUpdateExitHelper$"}
			}
			if err = next.Update(t.Context(), next.executable); !errors.Is(err, ErrUpdateRolledBack) {
				t.Fatalf("update = %v", err)
			}
			after, err := m.readSelection()
			if err != nil || !after.same(before) {
				t.Fatalf("selection = %+v, %v", after, err)
			}
			if ready, err := m.probeReady(t.Context(), ""); !ready || err != nil {
				t.Fatalf("rollback not Ready: %v", err)
			}
			assertLaunchCount(t, marker, 2)
			if _, err = os.Stat(filepath.Join(m.app.directory, "update.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed rollback retained journal")
			}
			if err = m.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateExitHelper(t *testing.T) {}

// Recreate each durable checkpoint, including selection committed before cleanup.
func TestUpdateResumeCheckpoints(t *testing.T) {
	for _, phase := range []updatePhase{updateStopping, updateStarting, updateVerifying, updateCommitting} {
		t.Run(string(phase), func(t *testing.T) {
			m, _ := testManager(t)
			if err := m.EnsureRunning(t.Context()); err != nil {
				t.Fatal(err)
			}
			previous, err := m.readSelection()
			if err != nil {
				t.Fatal(err)
			}
			old, err := readExecution(m.app.directory)
			if err != nil {
				t.Fatal(err)
			}
			next := updateCandidate(t, m)
			candidate, err := prepareUpdateSelection(t, next)
			if err != nil {
				t.Fatal(err)
			}
			r := updateRecord{Phase: phase, Previous: previous, Candidate: candidate, Stopping: old}
			if phase != updateStopping {
				if err = m.stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				r.Stopping = executionRecord{}
				r.LaunchNonce = rand.Text()
				r.Deadline = time.Now().Add(startTimeout)
			}
			if phase == updateVerifying || phase == updateCommitting {
				if _, err = m.spawnRelease(t.Context(), candidate, r.LaunchNonce); err != nil {
					t.Fatal(err)
				}
			}
			if phase == updateCommitting {
				r.Verified, err = m.verifyUpdate(t.Context(), r)
				if err != nil {
					t.Fatal(err)
				}
				if err = writeJSONRecord(m.app.directory, "current.json", candidate); err != nil {
					t.Fatal(err)
				}
				if err = m.clearUpdateLaunch(r.LaunchNonce); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.writeUpdate(r); err != nil {
				t.Fatal(err)
			}
			if err = m.EnsureRunning(t.Context()); err != nil {
				t.Fatal(err)
			}
			selected, err := m.readSelection()
			if err != nil || !selected.same(candidate) {
				t.Fatalf("resume selection: %+v, %v", selected, err)
			}
			if err = m.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateStopDoesNotLaunchReplacement(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	previous, _ := m.readSelection()
	old, _ := readExecution(m.app.directory)
	next := updateCandidate(t, m)
	candidate, err := prepareUpdateSelection(t, next)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.writeUpdate(updateRecord{Phase: updateStopping, Previous: previous, Candidate: candidate, Stopping: old}); err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 1)
	selected, err := m.readSelection()
	if err != nil || !selected.same(previous) {
		t.Fatalf("stop selection: %+v, %v", selected, err)
	}
	if err = m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 2)
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateUnknownLaunchIsRetained(t *testing.T) {
	m, _ := testManager(t)
	previous, err := prepareUpdateSelection(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeJSONRecord(m.app.directory, "current.json", previous); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	candidate, err := prepareUpdateSelection(t, next)
	if err != nil {
		t.Fatal(err)
	}
	r := updateRecord{Phase: updateVerifying, Previous: previous, Candidate: candidate, LaunchNonce: rand.Text(), Deadline: time.Now().Add(-time.Second)}
	if err = m.writeUpdate(r); err != nil {
		t.Fatal(err)
	}
	if err = writeJSONRecord(m.app.directory, "launch.json", launchRecord{LaunchNonce: r.LaunchNonce, Release: candidate.Release}); err != nil {
		t.Fatal(err)
	}
	if err = m.EnsureRunning(t.Context()); err == nil || errors.Is(err, ErrUpdateRolledBack) {
		t.Fatalf("unknown launch = %v", err)
	}
	if err = m.Stop(t.Context()); err == nil {
		t.Fatal("unknown launch reported stopped")
	}
	retained, err := m.readUpdate()
	if err != nil || retained.Rollback {
		t.Fatalf("unknown launch was discarded: %+v, %v", retained, err)
	}
}

func TestUpdateCLIHelper(t *testing.T) {
	source := os.Getenv("DAEMONKIT_UPDATE_SOURCE")
	if source == "" {
		return
	}
	m, err := NewManager(Options{AppID: "test", Version: "update-cli"}, "-test.run=^TestManagerDaemonHelper$")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Update(t.Context(), source); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSurvivesLauncherExit(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	gate := filepath.Join(filepath.Dir(marker), "update-gate")
	t.Setenv("DAEMONKIT_TEST_GATE", gate)
	t.Setenv("DAEMONKIT_UPDATE_SOURCE", next.executable)
	command := exec.Command(m.executable, "-test.run=^TestUpdateCLIHelper$", "-test.timeout=30s")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		r, err := m.readUpdate()
		if err == nil && r.Phase == updateVerifying {
			pending, err := readLaunch(m.app.directory)
			if err == nil && pending.PID > 0 {
				break
			}
		}
		if ctx.Err() != nil {
			t.Fatal("updater did not reach verification")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 2)
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePreparationFailurePreservesDaemon(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Update(t.Context(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing candidate accepted")
	}
	after, err := readExecution(m.app.directory)
	if err != nil || before != after {
		t.Fatalf("incumbent changed: %+v, %v", after, err)
	}
	assertLaunchCount(t, marker, 1)
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCancellationPreservesCandidate(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	gate := filepath.Join(filepath.Dir(marker), "update-gate")
	t.Setenv("DAEMONKIT_TEST_GATE", gate)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- next.Update(ctx, next.executable) }()
	deadline := time.Now().Add(10 * time.Second)
	var pending launchRecord
	for {
		r, err := m.readUpdate()
		if err == nil && r.Phase == updateVerifying {
			pending, err = readLaunch(m.app.directory)
			if err == nil && pending.PID > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			err := <-done
			t.Fatalf("update did not reach verification: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if alive, err := processMatches(pending.PID, pending.ProcessIdentity); err != nil || !alive {
		t.Fatalf("cancellation killed candidate: %v", err)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 2)
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRollbackFailureRetainsJournal(t *testing.T) {
	m, _ := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	previous, err := m.readSelection()
	if err != nil {
		t.Fatal(err)
	}
	previous.Args = []string{"-test.run=^TestUpdateExitHelper$"}
	if err = writeJSONRecord(m.app.directory, "current.json", previous); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	if err = os.WriteFile(next.executable, []byte("invalid executable"), 0700); err != nil {
		t.Fatal(err)
	}
	err = next.Update(t.Context(), next.executable)
	if err == nil || errors.Is(err, ErrUpdateRolledBack) {
		t.Fatalf("failed rollback = %v", err)
	}
	r, err := m.readUpdate()
	if err != nil || !r.Rollback {
		t.Fatalf("rollback journal lost: %+v, %v", r, err)
	}
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateUnreadableStateBlocksLifecycle(t *testing.T) {
	for _, kind := range []string{"invalid-phase", "missing-selection", "changed-selection"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := testManager(t)
			previous, err := prepareUpdateSelection(t, m)
			if err != nil {
				t.Fatal(err)
			}
			next := updateCandidate(t, m)
			candidate, err := prepareUpdateSelection(t, next)
			if err != nil {
				t.Fatal(err)
			}
			if err = writeJSONRecord(m.app.directory, "current.json", previous); err != nil {
				t.Fatal(err)
			}
			r := updateRecord{Phase: updateStopping, Previous: previous, Candidate: candidate}
			switch kind {
			case "invalid-phase":
				r.Phase = "bad"
			case "missing-selection":
				if err = os.Remove(filepath.Join(m.app.directory, "current.json")); err != nil {
					t.Fatal(err)
				}
			case "changed-selection":
				if err = writeJSONRecord(m.app.directory, "current.json", candidate); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.writeUpdate(r); err != nil {
				t.Fatal(err)
			}
			for _, call := range []func(context.Context) error{m.EnsureRunning, m.Stop, m.Restart, func(ctx context.Context) error { return m.Update(ctx, m.executable) }} {
				if err = call(t.Context()); err == nil {
					t.Fatal("unverifiable update accepted")
				}
			}
			if _, err = os.Stat(filepath.Join(m.app.directory, "update.json")); err != nil {
				t.Fatal("journal discarded")
			}
		})
	}
}

func TestUpdateConcurrentLifecycle(t *testing.T) {
	m, _ := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	done := make(chan error, 12)
	for i := range 12 {
		go func() {
			switch i % 4 {
			case 0:
				done <- next.Update(t.Context(), next.executable)
			case 1:
				done <- m.EnsureRunning(t.Context())
			case 2:
				done <- m.Restart(t.Context())
			case 3:
				done <- m.Stop(t.Context())
			}
		}()
	}
	for range 12 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateResumesStopAfterLaunchCleanup(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	previous, err := m.readSelection()
	if err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	candidate, err := prepareUpdateSelection(t, next)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	nonce := rand.Text()
	pending, err := m.spawnRelease(t.Context(), candidate, nonce)
	if err != nil {
		t.Fatal(err)
	}
	r := updateRecord{Phase: updateVerifying, Previous: previous, Candidate: candidate, LaunchNonce: nonce, Deadline: time.Now().Add(startTimeout), StopRequested: true}
	process, err := m.verifyUpdate(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.StopTarget = &process
	if err = m.writeUpdate(r); err != nil {
		t.Fatal(err)
	}
	if err = m.stopUpdateGeneration(t.Context(), process); err != nil {
		t.Fatal(err)
	}
	if err = m.clearUpdateLaunch(pending.LaunchNonce); err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLaunchCount(t, marker, 2)
	if _, err = m.readUpdate(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stop journal retained: %v", err)
	}
}

func TestUpdateResumesBeforeApplyingDifferentCandidate(t *testing.T) {
	m, marker := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	previous, err := m.readSelection()
	if err != nil {
		t.Fatal(err)
	}
	old, err := readExecution(m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	first := updateCandidate(t, m)
	candidate, err := prepareUpdateSelection(t, first)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.writeUpdate(updateRecord{Phase: updateStopping, Previous: previous, Candidate: candidate, Stopping: old}); err != nil {
		t.Fatal(err)
	}
	second := updateCandidate(t, first)
	if err = second.Update(t.Context(), second.executable); err != nil {
		t.Fatal(err)
	}
	want, err := prepareUpdateSelection(t, second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.readSelection()
	if err != nil || !got.same(want) {
		t.Fatalf("different candidate = %+v, %v", got, err)
	}
	assertLaunchCount(t, marker, 3)
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDeadLaunchStaysStopped(t *testing.T) {
	m, marker := testManager(t)
	previous, err := prepareUpdateSelection(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeJSONRecord(m.app.directory, "current.json", previous); err != nil {
		t.Fatal(err)
	}
	pending := launchRecord{LaunchNonce: rand.Text(), Release: previous.Release, PID: os.Getpid(), ProcessIdentity: "a-dead-generation"}
	if err = writeJSONRecord(m.app.directory, "launch.json", pending); err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	if err = next.Update(t.Context(), next.executable); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("update restarted a dead launch")
	}
	selected, err := m.readSelection()
	if err != nil || selected.Release == previous.Release {
		t.Fatalf("candidate not selected: %+v, %v", selected, err)
	}
}

func prepareUpdateSelection(t *testing.T, m *Manager) (releaseSelection, error) {
	t.Helper()
	unlock, err := lockOperation(t.Context(), m.app.directory)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := m.prepareSelection(m.executable)
	return selection, errors.Join(err, unlock())
}

func TestUpdateHungCandidate(t *testing.T) {
	m, _ := testManager(t)
	if err := m.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	previous, err := m.readSelection()
	if err != nil {
		t.Fatal(err)
	}
	next := updateCandidate(t, m)
	next.args = []string{"-test.run=^TestUpdateHangHelper$"}
	candidate, err := prepareUpdateSelection(t, next)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	nonce := rand.Text()
	if _, err = m.spawnRelease(t.Context(), candidate, nonce); err != nil {
		t.Fatal(err)
	}
	r := updateRecord{Phase: updateVerifying, Previous: previous, Candidate: candidate, LaunchNonce: nonce, Deadline: time.Now().Add(-time.Second)}
	if err = m.writeUpdate(r); err != nil {
		t.Fatal(err)
	}
	err = m.EnsureRunning(t.Context())
	if runtime.GOOS == "darwin" {
		if err == nil || errors.Is(err, ErrUpdateRolledBack) || !strings.Contains(err.Error(), "rollback incomplete") {
			t.Fatalf("unsafe rollback result: %v", err)
		}
		r, readErr := m.readUpdate()
		if readErr != nil || !r.Rollback || r.Phase != updateStopping {
			t.Fatalf("rollback lost: %+v, %v", r, readErr)
		}
	} else {
		if !errors.Is(err, ErrUpdateRolledBack) {
			t.Fatalf("hung candidate rollback = %v", err)
		}
		if err = m.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateHangHelper(t *testing.T) {
	if os.Getenv("DAEMONKIT_MANAGER_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}
