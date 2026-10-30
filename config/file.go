// Package config provides typed TOML files and an optional daemon service.
// Saving a file does not reload a running service. Configuration and secrets are
// never automatically logged, snapshotted, or sent over RPC.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const maxFileSize = 1 << 20

// Options describes one file. Path overrides AppID's default user config path.
// Missing default files load Defaults; a missing explicit Path is an error.
type Options[T any] struct {
	AppID string
	Path  string
	// Defaults must return a fresh value, including fresh maps and slices.
	Defaults func() T
	// Validate checks a complete decoded candidate without changing live state.
	// It must honor ctx and return errors safe to display (no secret values).
	Validate func(context.Context, T) error
}

// File has immutable options and is safe for concurrent use if its callbacks are.
// Writes are last-save-wins; concurrent edits can overwrite each other's changes.
type File[T any] struct {
	path     string
	optional bool
	defaults func() T
	validate func(context.Context, T) error
}

func New[T any](options Options[T]) (*File[T], error) {
	path := options.Path
	optional := path == ""
	if optional {
		id := options.AppID
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\:`) {
			return nil, errors.New("invalid config app ID")
		}
		base, err := userConfigDir()
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(base) {
			return nil, errors.New("user config directory must be absolute")
		}
		path = filepath.Join(base, id, "config.toml")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &File[T]{path: absolute, optional: optional, defaults: options.Defaults, validate: options.Validate}, nil
}

func userConfigDir() (string, error) {
	if runtime.GOOS == "windows" {
		return os.UserConfigDir()
	}
	if path := os.Getenv("XDG_CONFIG_HOME"); path != "" {
		if !filepath.IsAbs(path) {
			return "", errors.New("XDG_CONFIG_HOME must be absolute")
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

func (f *File[T]) Path() string { return f.path }

// Load returns an independently decoded candidate, never the service's live state.
func (f *File[T]) Load(ctx context.Context) (T, error) {
	data, err := f.read(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	return f.decode(ctx, data)
}

func (f *File[T]) decode(ctx context.Context, data []byte) (T, error) {
	var value T
	if err := ctx.Err(); err != nil {
		return value, err
	}
	if len(data) > maxFileSize {
		return value, errors.New("config exceeds 1 MiB")
	}
	if f.defaults != nil {
		value = f.defaults()
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&value); err != nil {
		// Decoder errors may quote credentials or include source excerpts.
		var parse *toml.DecodeError
		if errors.As(err, &parse) {
			line, column := parse.Position()
			return value, fmt.Errorf("invalid TOML or config schema at line %d, column %d", line, column)
		}
		return value, errors.New("invalid TOML or config schema")
	}
	if f.validate != nil {
		if err := f.validate(ctx, value); err != nil {
			return value, err
		}
	}
	return value, ctx.Err()
}

func (f *File[T]) read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := readRegular(f.path)
	if errors.Is(err, os.ErrNotExist) && f.optional {
		return []byte{}, nil
	}
	return data, err
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("config must be a regular file, not a symlink")
	}
	file, err := openRead(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("config file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, errors.New("config exceeds 1 MiB")
	}
	return data, nil
}

// Save validates the entire document, then replaces the file. It does not apply
// runtime state. Cancellation after replacement cannot undo a successful save.
// The parent directory must be application-controlled; symlink targets are refused.
func (f *File[T]) Save(ctx context.Context, data []byte) error {
	if len(data) > maxFileSize {
		return errors.New("config exceeds 1 MiB")
	}
	data = bytes.Clone(data)
	if _, err := f.decode(ctx, data); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	info, err := os.Lstat(f.path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("config must be a regular file, not a symlink")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	defer func() { _ = temp.Close() }()
	if err := privateFile(temp); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reject a symlink introduced while preparing the replacement, without CAS.
	if info, err := os.Lstat(f.path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("config target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temp.Name(), f.path)
}
