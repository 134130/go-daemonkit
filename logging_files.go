package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

func prepareLogDirectory(instance string) (string, error) {
	dir := filepath.Join(instance, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		return "", fmt.Errorf("unsafe log directory: %w", os.ErrPermission)
	}
	return dir, nil
}

func checkLogPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("log must be a regular file: %s", path)
	}
	return nil
}

type rotatingLog struct {
	path string
	file *os.File
	size int64
	// The next rename to perform; retained on failure to avoid rotating twice.
	step int
}

func newRotatingLog(path string) (*rotatingLog, error) {
	r := &rotatingLog{path: path}
	return r, r.open()
}
func (r *rotatingLog) open() error {
	f, err := openLogFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return errors.Join(err, f.Close())
	}
	r.file, r.size = f, info.Size()
	return nil
}
func (r *rotatingLog) Write(p []byte) (int, error) {
	if r.step > 0 {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > logFileSize {
		r.step = logBackups
		f := r.file
		r.file = nil
		if err := f.Close(); err != nil {
			return 0, err
		}
		if err := r.rotate(); err != nil {
			return 0, err
		}
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}
func (r *rotatingLog) rotate() error {
	for r.step > 0 {
		source := r.path
		if r.step > 1 {
			source += "." + strconv.Itoa(r.step-1)
		}
		target := r.path + "." + strconv.Itoa(r.step)
		if err := checkLogPath(source); err != nil {
			return err
		}
		if err := checkLogPath(target); err != nil {
			return err
		}
		if err := renameFile(source, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		r.step--
	}
	return nil
}
func (r *rotatingLog) Close() error {
	if r.file == nil {
		return nil
	}
	f := r.file
	r.file = nil
	return f.Close()
}

// Raw descriptors stay attached to the same file. Concurrent native writes may
// be lost between the tail snapshot and truncate; size is only periodically bounded.
func pruneRawLog(path string) (err error) {
	f, err := openLogFile(path, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil || info.Size() <= logFileSize {
		return err
	}
	tail := make([]byte, rawLogTail)
	if _, err := f.ReadAt(tail, info.Size()-rawLogTail); err != nil {
		return err
	}
	backup := path + ".previous"
	if err := checkLogPath(backup); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".log-tail-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	n, writeErr := temp.Write(tail)
	if writeErr == nil && n != len(tail) {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return err
	}
	if err := renameFile(name, backup); err != nil {
		return err
	}
	return f.Truncate(0)
}
