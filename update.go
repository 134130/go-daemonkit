package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrUpdateRolledBack means the update failed and the previous release is Ready.
var ErrUpdateRolledBack = errors.New("update failed; previous release restored")

type updatePhase string

const (
	updateStopping   updatePhase = "stopping"
	updateStarting   updatePhase = "starting"
	updateVerifying  updatePhase = "verifying"
	updateCommitting updatePhase = "committing"
)

type updateRecord struct {
	StopTarget    *executionRecord `json:"stopTarget,omitempty"`
	Verified      executionRecord  `json:"verified"`
	Phase         updatePhase      `json:"phase"`
	Previous      releaseSelection `json:"previous"`
	Candidate     releaseSelection `json:"candidate"`
	Rollback      bool             `json:"rollback,omitempty"`
	StopRequested bool             `json:"stopRequested,omitempty"`
	Stopping      executionRecord  `json:"stopping"`
	LaunchNonce   string           `json:"launchNonce,omitempty"`
	Deadline      time.Time        `json:"deadline,omitempty"`
	Failure       string           `json:"failure,omitempty"`
}

func (r updateRecord) target() releaseSelection {
	if r.Rollback {
		return r.Previous
	}
	return r.Candidate
}

// nextUpdatePhase is the successful path; failures choose rollback explicitly.
func nextUpdatePhase(phase updatePhase) (updatePhase, error) {
	switch phase {
	case updateStopping:
		return updateStarting, nil
	case updateStarting:
		return updateVerifying, nil
	case updateVerifying:
		return updateCommitting, nil
	default:
		return "", fmt.Errorf("invalid update transition from %q", phase)
	}
}

func (m *Manager) readUpdate() (updateRecord, error) {
	var r updateRecord
	if err := readJSONRecord(m.app.directory, "update.json", &r); err != nil {
		return r, err
	}
	if !r.Previous.valid() || !r.Candidate.valid() {
		return r, fmt.Errorf("invalid update releases")
	}
	switch r.Phase {
	case updateStopping:
	case updateStarting, updateVerifying, updateCommitting:
		if len(r.LaunchNonce) < 16 || !validName(r.LaunchNonce) || r.Deadline.IsZero() {
			return r, fmt.Errorf("invalid update launch")
		}
	default:
		return r, fmt.Errorf("invalid update phase %q", r.Phase)
	}
	if r.Stopping.PID < 0 || (r.Stopping.PID == 0) != (r.Stopping.ProcessIdentity == "") || (r.Stopping.PID != 0 && r.Stopping.LaunchNonce == "") {
		return r, fmt.Errorf("invalid update process")
	}
	if r.StopTarget != nil && (!r.StopRequested || r.StopTarget.PID < 0 || (r.StopTarget.PID == 0) != (r.StopTarget.ProcessIdentity == "") || (r.StopTarget.PID != 0 && r.StopTarget.LaunchNonce == "")) {
		return r, fmt.Errorf("invalid update stop target")
	}
	if r.Phase == updateCommitting && (r.Verified.PID <= 0 || r.Verified.ProcessIdentity == "" || r.Verified.LaunchNonce != r.LaunchNonce || r.Verified.Release != r.target().Release) {
		return r, fmt.Errorf("invalid verified update process")
	}
	selected, err := m.readSelection()
	if err != nil {
		return r, err
	}
	if !selected.same(r.Previous) && (r.Phase != updateCommitting || !selected.same(r.target())) {
		return r, fmt.Errorf("release selection changed during update")
	}
	return r, nil
}

func (m *Manager) writeUpdate(r updateRecord) error {
	return writeJSONRecord(m.app.directory, "update.json", r)
}

// Update copies executablePath into managed storage and selects it with this
// Manager's daemon arguments. The caller owns downloading and verifying the
// executable's source, integrity, and requested version, and must keep it unchanged
// until Update returns. The source can then be removed, including after cancellation.
// A running managed daemon is replaced and verified; a stopped instance remains
// stopped without startup verification. Cancellation after
// shutdown intent is recorded leaves a journal for the next lifecycle call to resume.
// Rollback restores processes and binaries, never application data.
func (m *Manager) Update(ctx context.Context, executablePath string) (err error) {
	unlock, err := lockOperation(ctx, m.app.directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if _, err := m.resumeUpdate(ctx, false); err != nil {
		return err
	}
	candidate, err := m.prepareSelection(executablePath)
	if err != nil {
		return err
	}
	previous, err := m.readSelection()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && candidate.Release == previous.Release {
		return nil
	}
	// Observe an existing launch without starting a replacement for a dead child.
	if pending, err := readLaunch(m.app.directory); err == nil {
		alive := true
		if pending.PID != 0 {
			alive, err = processMatches(pending.PID, pending.ProcessIdentity)
			if err != nil {
				return err
			}
		}
		if !alive {
			if err = removeLaunch(m.app.directory, pending.LaunchNonce); err != nil {
				return err
			}
		} else {
			wait, cancel := context.WithTimeout(ctx, startTimeout)
			var child *launchRecord
			if pending.PID != 0 {
				child = &pending
			}
			err = m.waitReady(wait, pending.LaunchNonce, child)
			cancel()
			if err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	current, err := readExecution(m.app.directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	running := false
	if err == nil {
		running, err = processMatches(current.PID, current.ProcessIdentity)
		if err != nil {
			return err
		}
	}
	if !running {
		release, lockErr := lockInstance(m.app.directory)
		if lockErr != nil {
			return lockErr
		}
		defer func() { err = errors.Join(err, release()) }()
		if err = ctx.Err(); err != nil {
			return err
		}
		return writeJSONRecord(m.app.directory, "current.json", candidate)
	}
	if !previous.valid() || current.Release != previous.Release {
		return fmt.Errorf("running daemon is not the selected managed release")
	}
	if _, err = m.releasePath(previous); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, startTimeout)
	err = m.waitReady(wait, current.LaunchNonce, nil)
	cancel()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	r := updateRecord{Phase: updateStopping, Previous: previous, Candidate: candidate, Stopping: current}
	if err = m.writeUpdate(r); err != nil {
		return err
	}
	return m.runUpdate(ctx, r)
}

func (m *Manager) resumeUpdate(ctx context.Context, stop bool) (bool, error) {
	if _, err := os.Lstat(filepath.Join(m.app.directory, "update.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	r, err := m.readUpdate()
	if err != nil {
		return true, err
	}
	if stop && !r.StopRequested {
		r.StopRequested = true
		if err = m.writeUpdate(r); err != nil {
			return true, err
		}
	}
	return true, m.runUpdate(ctx, r)
}

func (m *Manager) runUpdate(ctx context.Context, r updateRecord) (err error) {
	defer func() {
		if err != nil && r.Rollback && !errors.Is(err, ErrUpdateRolledBack) {
			err = fmt.Errorf("update failed (%s); rollback incomplete: %w", r.Failure, err)
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.StopRequested {
			return m.stopUpdate(ctx, r)
		}
		switch r.Phase {
		case updateStopping:
			if err := m.stopUpdateGeneration(ctx, r.Stopping); err != nil {
				return err
			}
			if err := m.clearUpdateLaunch(r.LaunchNonce); err != nil {
				return err
			}
			r.LaunchNonce = rand.Text()
			r.Deadline = time.Now().Add(startTimeout)
			r.Stopping = executionRecord{}
		case updateStarting:
			pending, err := readLaunch(m.app.directory)
			if errors.Is(err, os.ErrNotExist) {
				release, lockErr := lockInstance(m.app.directory)
				if lockErr != nil {
					return lockErr
				}
				if lockErr = release(); lockErr != nil {
					return lockErr
				}
				_, err = m.spawnRelease(ctx, r.target(), r.LaunchNonce)
			} else if err == nil && (pending.LaunchNonce != r.LaunchNonce || pending.Release != r.target().Release) {
				return fmt.Errorf("update launch ownership changed")
			}
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// A retained reservation can mean spawn succeeded before bookkeeping failed.
				if _, readErr := readLaunch(m.app.directory); readErr == nil {
					return err
				} else if !errors.Is(readErr, os.ErrNotExist) {
					return errors.Join(err, readErr)
				}
				var rollbackErr error
				r, rollbackErr = m.beginRollback(r, executionRecord{}, err)
				if rollbackErr != nil {
					return rollbackErr
				}
				continue
			}
		case updateVerifying:
			process, err := m.verifyUpdate(ctx, r)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if process.PID == 0 {
					return err
				}
				var rollbackErr error
				r, rollbackErr = m.beginRollback(r, process, err)
				if rollbackErr != nil {
					return rollbackErr
				}
				continue
			}
			r.Verified = process
		case updateCommitting:
			if err := writeJSONRecord(m.app.directory, "current.json", r.target()); err != nil {
				return err
			}
			if err := m.clearUpdateLaunch(r.LaunchNonce); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(m.app.directory, "update.json")); err != nil {
				return err
			}
			if r.Rollback {
				return fmt.Errorf("%w: %s", ErrUpdateRolledBack, r.Failure)
			}
			return nil
		default:
			return fmt.Errorf("invalid update phase %q", r.Phase)
		}
		next, err := nextUpdatePhase(r.Phase)
		if err != nil {
			return err
		}
		r.Phase = next
		if err = m.writeUpdate(r); err != nil {
			return err
		}
	}
}

func (m *Manager) beginRollback(r updateRecord, process executionRecord, cause error) (updateRecord, error) {
	if r.Rollback {
		return r, cause
	}
	r.Rollback = true
	r.Phase = updateStopping
	r.Stopping = process
	r.Failure = cause.Error()
	if len(r.Failure) > 1024 {
		r.Failure = r.Failure[:1024]
	}
	return r, m.writeUpdate(r)
}

// updateProcess never interprets a missing PID as proof that spawn failed.
func (m *Manager) updateProcess(r updateRecord) (executionRecord, error) {
	runtime, err := readExecution(m.app.directory)
	if err == nil {
		alive, checkErr := processMatches(runtime.PID, runtime.ProcessIdentity)
		if checkErr != nil {
			return executionRecord{}, checkErr
		}
		if alive {
			if runtime.LaunchNonce != r.LaunchNonce || runtime.Release != r.target().Release {
				return executionRecord{}, fmt.Errorf("update runtime ownership changed")
			}
			return runtime, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return executionRecord{}, err
	}
	pending, err := readLaunch(m.app.directory)
	if err != nil {
		return executionRecord{}, err
	}
	if pending.LaunchNonce != r.LaunchNonce || pending.Release != r.target().Release {
		return executionRecord{}, fmt.Errorf("update launch ownership changed")
	}
	if pending.PID == 0 {
		return executionRecord{}, nil
	}
	return executionRecord{PID: pending.PID, ProcessIdentity: pending.ProcessIdentity, LaunchNonce: pending.LaunchNonce, Release: pending.Release}, nil
}

func (m *Manager) verifyUpdate(ctx context.Context, r updateRecord) (executionRecord, error) {
	wait, cancel := context.WithDeadline(ctx, r.Deadline)
	defer cancel()
	var process executionRecord
	for {
		observed, err := m.updateProcess(r)
		if err != nil {
			return executionRecord{}, err
		}
		process = observed
		if process.PID != 0 {
			alive, err := processMatches(process.PID, process.ProcessIdentity)
			if err != nil {
				return executionRecord{}, err
			}
			if !alive {
				return process, fmt.Errorf("daemon exited before update Ready")
			}
			ready, err := m.probeReady(wait, r.LaunchNonce)
			if err != nil {
				return process, err
			}
			if ready {
				return process, nil
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			if process.PID == 0 {
				return process, fmt.Errorf("update launch outcome unknown: %w", wait.Err())
			}
			return process, wait.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) clearUpdateLaunch(nonce string) error {
	if nonce == "" {
		return nil
	}
	return removeLaunch(m.app.directory, nonce)
}

func (m *Manager) stopUpdateGeneration(ctx context.Context, expected executionRecord) error {
	if expected.PID == 0 {
		return nil
	}
	alive, err := processMatches(expected.PID, expected.ProcessIdentity)
	if err != nil {
		return err
	}
	if !alive {
		var stopping executionRecord
		if err := readJSONRecord(m.app.directory, "stopping.json", &stopping); err == nil {
			if stopping.LaunchNonce != expected.LaunchNonce {
				return fmt.Errorf("shutdown ownership changed")
			}
			return m.finishStop(ctx, stopping)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	current, err := readExecution(m.app.directory)
	if err == nil && current.LaunchNonce != expected.LaunchNonce {
		return fmt.Errorf("update shutdown ownership changed")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ready, probeErr := m.probeReady(ctx, expected.LaunchNonce)
	if ready && probeErr == nil {
		return m.stop(ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A known child may hang before IPC is ready. Retain the same ownership checks
	// and platform restrictions as ordinary forced shutdown.
	if err := writeJSONRecord(m.app.directory, "stopping.json", expected); err != nil {
		return err
	}
	return m.finishStop(ctx, expected)
}

func (m *Manager) stopUpdate(ctx context.Context, r updateRecord) (err error) {
	process := r.Stopping
	if r.StopTarget != nil {
		process = *r.StopTarget
	} else if r.Phase == updateCommitting {
		process = r.Verified
	} else if r.Phase != updateStopping {
		var err error
		process, err = m.updateProcess(r)
		if err != nil {
			if r.Phase != updateStarting || !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if process.PID == 0 {
			return fmt.Errorf("update launch outcome unknown; cannot confirm stop")
		}
	}
	if r.StopTarget == nil {
		r.StopTarget = &process
		if err := m.writeUpdate(r); err != nil {
			return err
		}
	}
	if err := m.stopUpdateGeneration(ctx, process); err != nil {
		return err
	}
	release, err := lockInstance(m.app.directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	selected := r.Previous
	if r.Phase == updateCommitting {
		selected = r.target()
	}
	if err := writeJSONRecord(m.app.directory, "current.json", selected); err != nil {
		return err
	}
	if err := m.clearUpdateLaunch(r.LaunchNonce); err != nil {
		return err
	}
	return os.Remove(filepath.Join(m.app.directory, "update.json"))
}
