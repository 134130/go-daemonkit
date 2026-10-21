package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
)

const startTimeout = 30 * time.Second
const launchNonceEnv = "GO_DAEMONKIT_LAUNCH_NONCE"

// Manager runs the selected release in its application's daemon mode.
// It never replaces an already running, protocol-compatible daemon.
type Manager struct {
	app        *App
	executable string
	args       []string
}

// NewManager takes the arguments that make this executable call App.Run.
// Constructing a Manager does not copy files or start a process.
func NewManager(options Options, daemonArgs ...string) (*Manager, error) {
	if len(daemonArgs) == 0 {
		return nil, fmt.Errorf("daemon mode arguments are required")
	}
	app, err := New(options)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	return &Manager{app: app, executable: executable, args: slices.Clone(daemonArgs)}, nil
}

// EnsureRunning serializes starts and waits for a compatible Ready handshake.
// Cancellation or timeout ends only this caller's wait, not the daemon's life.
func (m *Manager) EnsureRunning(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	unlock, err := lockOperation(ctx, m.app.directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if _, err := m.resumeUpdate(ctx, false); err != nil {
		return err
	}
	return m.ensureRunning(ctx)
}

// ensureRunning requires the operation lock.
func (m *Manager) ensureRunning(ctx context.Context) error {
	if _, err := os.Lstat(filepath.Join(m.app.directory, "stopping.json")); err == nil {
		if err := m.stop(ctx); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	pending, pendingErr := readLaunch(m.app.directory)
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}
	expected := ""
	if pendingErr == nil {
		expected = pending.LaunchNonce
	}
	if ready, err := m.probe(ctx, expected); ready || err != nil {
		return err
	}
	if pendingErr == nil {
		if pending.PID == 0 {
			// A prior launcher may have died between spawn and recording its child.
			// Wait for that generation; guessing would risk a second spawn.
			if err := m.waitReady(ctx, pending.LaunchNonce, nil); err != nil {
				return fmt.Errorf("launch outcome is unknown; refusing another spawn: %w", err)
			}
			return nil
		}
		alive, err := processMatches(pending.PID, pending.ProcessIdentity)
		if err != nil {
			return err
		}
		if alive {
			return m.waitReady(ctx, pending.LaunchNonce, &pending)
		}
		if err := removeLaunch(m.app.directory, pending.LaunchNonce); err != nil {
			return err
		}
	}
	if record, err := readExecution(m.app.directory); err == nil {
		alive, err := processMatches(record.PID, record.ProcessIdentity)
		if err != nil {
			return err
		}
		if alive {
			return m.waitReady(ctx, record.LaunchNonce, nil)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A foreground process can hold its runtime lock before it publishes a record.
	releaseRuntime, err := lockInstance(m.app.directory)
	if errors.Is(err, ErrAlreadyRunning) {
		return m.waitReady(ctx, "", nil)
	}
	if err != nil {
		return err
	}
	if err := releaseRuntime(); err != nil {
		return err
	}

	selection, err := m.readSelection()
	if errors.Is(err, os.ErrNotExist) {
		selection, err = m.prepareSelection(m.executable)
		if err == nil {
			err = writeJSONRecord(m.app.directory, "current.json", selection)
		}
	}
	if err != nil {
		return err
	}
	pending, err = m.spawnRelease(ctx, selection, rand.Text())
	if err != nil {
		return err
	}
	return m.waitReady(ctx, pending.LaunchNonce, &pending)
}

// spawnRelease records intent before spawn. Unknown outcomes retain the reservation.
func (m *Manager) spawnRelease(ctx context.Context, selection releaseSelection, nonce string) (launchRecord, error) {
	pending := launchRecord{LaunchNonce: nonce, Release: selection.Release}
	path, err := m.releasePath(selection)
	if err != nil {
		return pending, err
	}
	if err = ctx.Err(); err != nil {
		return pending, err
	}
	if err = writeJSONRecord(m.app.directory, "launch.json", pending); err != nil {
		return pending, err
	}
	command := exec.Command(path, selection.Args...)
	command.Dir = m.app.directory
	command.Env = append(os.Environ(), launchNonceEnv+"="+nonce)
	configureDetached(command)
	log, err := os.OpenFile(filepath.Join(m.app.directory, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return pending, errors.Join(err, removeLaunch(m.app.directory, nonce))
	}
	command.Stdout, command.Stderr = log, log
	err = command.Start()
	_ = log.Close()
	if err != nil {
		return pending, errors.Join(err, removeLaunch(m.app.directory, nonce))
	}
	go func() { _ = command.Wait() }()
	pending.PID = command.Process.Pid
	pending.ProcessIdentity, err = processIdentity(pending.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pending, errors.Join(fmt.Errorf("daemon exited during startup: %w", err), removeLaunch(m.app.directory, nonce))
		}
		return pending, fmt.Errorf("read spawned process identity (launch reservation retained): %w", err)
	}
	if err = writeJSONRecord(m.app.directory, "launch.json", pending); err != nil {
		return pending, err
	}
	return pending, nil
}

func (m *Manager) probe(ctx context.Context, expected string) (bool, error) {
	ready, err := m.probeReady(ctx, expected)
	if ready && err == nil && expected != "" {
		err = removeLaunch(m.app.directory, expected)
	}
	return ready, err
}

func (m *Manager) probeReady(ctx context.Context, expected string) (bool, error) {
	params := initializeParams{m.app.options.AppID, m.app.options.InstanceID, protocolVersion, m.app.options.Version}
	conn, identity, err := connectLocal(ctx, m.app.endpoint, params)
	if err != nil {
		var protocolError *rpcError
		if errors.As(err, &protocolError) || errors.Is(err, errHandshake) {
			return false, err
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	_ = conn.CloseNow()
	if expected != "" && expected != identity.LaunchNonce {
		return false, fmt.Errorf("connected daemon differs from expected launch generation")
	}
	return true, nil
}

func (m *Manager) waitReady(ctx context.Context, nonce string, child *launchRecord) error {
	for {
		if ready, err := m.probe(ctx, nonce); ready || err != nil {
			return err
		}
		if child != nil {
			alive, err := processMatches(child.PID, child.ProcessIdentity)
			if err != nil {
				return err
			}
			if !alive {
				return errors.Join(fmt.Errorf("daemon exited before Ready; see daemon.log"), removeLaunch(m.app.directory, nonce))
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func processMatches(pid int, expected string) (bool, error) {
	identity, err := processIdentity(pid)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil && identity == expected, err
}

type launchRecord struct {
	Release         string `json:"release,omitempty"`
	LaunchNonce     string `json:"launchNonce"`
	PID             int    `json:"pid,omitempty"`
	ProcessIdentity string `json:"processIdentity,omitempty"`
}

func readLaunch(directory string) (launchRecord, error) {
	var record launchRecord
	if err := readJSONRecord(directory, "launch.json", &record); err != nil {
		return record, err
	}
	if len(record.LaunchNonce) < 16 || !validName(record.LaunchNonce) || record.PID < 0 || (record.PID == 0) != (record.ProcessIdentity == "") {
		return record, fmt.Errorf("invalid launch reservation")
	}
	return record, nil
}

func removeLaunch(directory, nonce string) error {
	record, err := readLaunch(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.LaunchNonce != nonce {
		return fmt.Errorf("launch reservation ownership changed")
	}
	return os.Remove(filepath.Join(directory, "launch.json"))
}
