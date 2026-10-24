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
	Closing         bool   `json:"closing,omitempty"`
	Release         string `json:"release,omitempty"`
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
	var record executionRecord
	if err := readJSONRecord(directory, "runtime.json", &record); err != nil {
		return record, err
	}
	if record.PID <= 0 || record.ProcessIdentity == "" || record.LaunchNonce == "" {
		return record, fmt.Errorf("incomplete execution record")
	}
	return record, nil
}

func readJSONRecord(directory, name string, target any) error {
	for range 3 {
		err := readJSONRecordOnce(directory, name, target)
		if !errors.Is(err, errRecordChanged) {
			return err
		}
	}
	return errRecordChanged
}

var errRecordChanged = errors.New("state record changed during read")

func readJSONRecordOnce(directory, name string, target any) error {
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 16<<10 {
		return fmt.Errorf("state record must be a regular file of at most 16 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errRecordChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, 16<<10))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeExecution(directory string, record executionRecord) error {
	return writeJSONRecord(directory, "runtime.json", record)
}

func writeJSONRecord(directory, name string, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 16<<10 {
		return fmt.Errorf("state record exceeds 16 KiB")
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
	return renameFile(file.Name(), filepath.Join(directory, name))
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
