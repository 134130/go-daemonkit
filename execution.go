package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type executionRecord struct {
	PID             int    `json:"pid"`
	ProcessIdentity string `json:"processIdentity"`
	LaunchNonce     string `json:"launchNonce"`
	SocketIdentity  string `json:"socketIdentity,omitempty"`
}

// Only lifecycle callers acquire the operation lock. The child holds the
// separate runtime lock so a parent's Ready wait cannot deadlock its startup.
func lockOperation(ctx context.Context, directory string) (func() error, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, err := lockNamedInstance(directory, "operation")
		if !errors.Is(err, ErrAlreadyRunning) {
			return unlock, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func readExecution(directory string) (executionRecord, error) {
	path := filepath.Join(directory, "runtime.json")
	info, err := os.Lstat(path)
	if err != nil {
		return executionRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 16<<10 {
		return executionRecord{}, fmt.Errorf("execution record must be a regular file of at most 16 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return executionRecord{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return executionRecord{}, err
	}
	if !os.SameFile(info, opened) {
		return executionRecord{}, fmt.Errorf("execution record changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16<<10))
	if err != nil {
		return executionRecord{}, err
	}
	var record executionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if record.PID <= 0 || record.ProcessIdentity == "" || record.LaunchNonce == "" {
		return record, fmt.Errorf("incomplete execution record")
	}
	return record, nil
}

func writeExecution(directory string, record executionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".runtime-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "runtime.json"))
}

// recoverExecution requires the runtime lock. Unverifiable ownership fails
// closed; neither a PID match nor a refused connection alone authorizes removal.
func recoverExecution(directory, endpoint string) error {
	record, err := readExecution(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	identity, err := processIdentity(record.PID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && identity == record.ProcessIdentity {
		return ErrAlreadyRunning
	}
	return recoverEndpoints(endpoint, record)
}

func removeExecution(directory string, expected executionRecord) error {
	current, err := readExecution(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("execution record ownership changed")
	}
	return os.Remove(filepath.Join(directory, "runtime.json"))
}
