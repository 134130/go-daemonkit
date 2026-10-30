package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// StatusState describes observations, not a persisted lifecycle state machine.
type StatusState string

const (
	StatusUnknown StatusState = "unknown"
	// StatusStopped means no live recorded process or reachable endpoint was observed.
	// A concurrent launch may not have published its process record yet.
	StatusStopped StatusState = "stopped"
	StatusRunning StatusState = "running"
	StatusReady   StatusState = "ready"
)

// Status is a best-effort snapshot. Concurrent lifecycle operations can change
// it immediately. Paths are available even when inspection returns an error.
type Status struct {
	// ClientVersion is the version supplied when constructing this Manager.
	ClientVersion string
	// UpdateToken is valid only for a Ready managed execution with matching selection.
	UpdateToken UpdateToken
	AppID       string
	InstanceID  string
	State       StatusState
	// PID and Release are populated only for a verified live process generation.
	PID int
	// Release is the managed executable's content hash; empty for foreground runs.
	Release string
	// Version and ProtocolVersion come from a successful readiness handshake.
	Version         string
	ProtocolVersion int
	StateDir        string
	// LogPath contains LogWriter output. Raw process output has separate paths.
	// Files may not exist before the first run.
	LogPath    string
	StdoutPath string
	StderrPath string
	Endpoint   string
}

// Status observes this instance without starting, stopping, recovering, or
// deleting anything. Running means a verified process exists, not that it is
// ready. Connection and inspection errors return partial information alongside
// the error; callers must not interpret an error as a stopped daemon.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	app := m.app
	result := Status{
		ClientVersion: app.options.Version,
		AppID:         app.options.AppID, InstanceID: app.options.InstanceID, State: StatusUnknown,
		StateDir: app.directory, LogPath: filepath.Join(app.directory, "logs", "daemon.log"),
		StdoutPath: filepath.Join(app.directory, "logs", "daemon.stdout"), StderrPath: filepath.Join(app.directory, "logs", "daemon.stderr"), Endpoint: app.endpoint,
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	record, err := readExecution(app.directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	alive := false
	if err == nil {
		alive, err = processMatches(record.PID, record.ProcessIdentity)
		if err != nil {
			return result, err
		}
	}
	uncertainLaunch := false
	if !alive {
		pending, err := readLaunch(app.directory)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if err == nil {
			uncertainLaunch = pending.PID == 0
			if pending.PID > 0 {
				alive, err = processMatches(pending.PID, pending.ProcessIdentity)
				if err != nil {
					return result, err
				}
				record = executionRecord{PID: pending.PID, ProcessIdentity: pending.ProcessIdentity, LaunchNonce: pending.LaunchNonce, Release: pending.Release}
			}
		}
	}
	if alive {
		result.State, result.PID, result.Release = StatusRunning, record.PID, record.Release
	}
	params := initializeParams{app.options.AppID, app.options.InstanceID, protocolVersion, app.options.Version}
	conn, identity, err := connectLocal(ctx, app.endpoint, params)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Only an absent endpoint or refused connection can establish stopped.
		// Permission, protocol, and timeout failures remain unknown or running.
		if !alive && !uncertainLaunch && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)) {
			result.State = StatusStopped
			return result, nil
		}
		return result, err
	}
	defer conn.CloseNow()
	// Bind the handshake to the current execution, including during replacement.
	current, err := readExecution(app.directory)
	if err != nil {
		return result, err
	}
	if current.LaunchNonce != identity.LaunchNonce {
		return result, fmt.Errorf("status: execution changed during handshake")
	}
	alive, err = processMatches(current.PID, current.ProcessIdentity)
	if err != nil {
		return result, err
	}
	if !alive {
		return result, fmt.Errorf("status: daemon exited during handshake")
	}
	result.State, result.PID, result.Release = StatusReady, current.PID, current.Release
	result.Version, result.ProtocolVersion = identity.ServerVersion, identity.ProtocolVersion
	if current.Release != "" {
		selection, err := m.readSelection()
		if err != nil {
			return result, err
		}
		if selection.Release == current.Release && !current.Closing {
			result.UpdateToken = UpdateToken{directory: app.directory, execution: current, selection: selection}
		}
	}
	return result, nil
}
