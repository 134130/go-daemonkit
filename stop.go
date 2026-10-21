package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stopGrace = 5 * time.Second

// Stop requests shutdown and waits for the recorded process generation to exit.
// Cancellation ends waiting; it does not undo an accepted shutdown.
func (m *Manager) Stop(ctx context.Context) (err error) {
	unlock, err := lockOperation(ctx, m.app.directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if found, err := m.resumeUpdate(ctx, true); found || err != nil {
		return err
	}
	return m.stop(ctx)
}

// Restart holds the operation lock across shutdown and the next Ready handshake.
func (m *Manager) Restart(ctx context.Context) (err error) {
	unlock, err := lockOperation(ctx, m.app.directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if _, err := m.resumeUpdate(ctx, false); err != nil {
		return err
	}
	if err := m.stop(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	return m.ensureRunning(ctx)
}

func (m *Manager) stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stopping executionRecord
	if err := readJSONRecord(m.app.directory, "stopping.json", &stopping); err == nil {
		if stopping.PID <= 0 || stopping.ProcessIdentity == "" || stopping.LaunchNonce == "" {
			return fmt.Errorf("invalid stopping record")
		}
		return m.finishStop(ctx, stopping)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	record, err := readExecution(m.app.directory)
	if errors.Is(err, os.ErrNotExist) {
		pending, pendingErr := readLaunch(m.app.directory)
		if pendingErr == nil {
			return fmt.Errorf("daemon startup is pending (%s); retry after Ready", pending.LaunchNonce)
		}
		if !errors.Is(pendingErr, os.ErrNotExist) {
			return pendingErr
		}
		unlock, lockErr := lockInstance(m.app.directory)
		if lockErr != nil {
			return lockErr
		}
		return unlock()
	}
	if err != nil {
		return err
	}
	alive, err := processMatches(record.PID, record.ProcessIdentity)
	if err != nil || !alive {
		return err
	}
	params := initializeParams{m.app.options.AppID, m.app.options.InstanceID, protocolVersion, m.app.options.Version}
	conn, identity, err := connectLocal(ctx, m.app.endpoint, params)
	if err != nil {
		alive, checkErr := processMatches(record.PID, record.ProcessIdentity)
		if checkErr == nil && !alive {
			return nil
		}
		return errors.Join(err, checkErr)
	}
	defer conn.CloseNow()
	if identity.LaunchNonce != record.LaunchNonce {
		return fmt.Errorf("shutdown generation does not match execution record")
	}
	if err := writeJSONRecord(m.app.directory, "stopping.json", record); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := writeRPC(requestCtx, conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`"shutdown"`), Method: "shutdown"}); err != nil {
		return err
	}
	response, responseErr := readRPC(requestCtx, conn)
	if responseErr == nil {
		if response.Error != nil {
			return errors.Join(response.Error, os.Remove(filepath.Join(m.app.directory, "stopping.json")))
		}
		if string(response.ID) != `"shutdown"` || response.Method != "" || len(response.Params) != 0 || string(response.Result) != "null" {
			return fmt.Errorf("invalid shutdown response")
		}
	}
	// A lost response cannot revoke shutdown; the recorded generation remains the target.
	return m.finishStop(ctx, record)
}

func (m *Manager) finishStop(ctx context.Context, record executionRecord) error {
	grace, endGrace := context.WithTimeout(ctx, stopGrace)
	defer endGrace()
	err := waitProcessExit(grace, record)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		current, readErr := readExecution(m.app.directory)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if readErr == nil && (current.PID != record.PID || current.ProcessIdentity != record.ProcessIdentity || current.LaunchNonce != record.LaunchNonce) {
			return fmt.Errorf("execution ownership changed during shutdown")
		}
		if err := killOwnedProcess(record); err != nil {
			return err
		}
		final, cancel := context.WithTimeout(ctx, stopGrace)
		defer cancel()
		err = waitProcessExit(final, record)
	}
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(m.app.directory, "stopping.json"))
}

func waitProcessExit(ctx context.Context, record executionRecord) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		alive, err := processMatches(record.PID, record.ProcessIdentity)
		if err != nil || !alive {
			return err
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
