package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// EditError preserves an unapplied document for manual recovery or re-editing.
// The caller is responsible for removing Path and its private parent directory.
type EditError struct {
	Path string
	Err  error
}

func (e *EditError) Error() string {
	return fmt.Sprintf("config edit failed; draft retained at %s: %v", e.Path, e.Err)
}
func (e *EditError) Unwrap() error { return e.Err }

// Edit opens a private temporary copy, validates it, and saves with last-save-wins.
// command is an executable plus arguments; the draft path is appended as one arg.
// With no command, VISUAL then EDITOR is interpreted as a trusted shell command;
// otherwise vi (Unix) or notepad.exe (Windows) is used. GUI editors need wait mode.
// The terminal is inherited. Success/no change removes the draft; failure retains
// it in EditError. Edit never applies runtime configuration.
func (f *File[T]) Edit(ctx context.Context, command ...string) error {
	original, err := f.read(ctx)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "daemonkit-config-*")
	if err != nil {
		return err
	}
	if err := privateDirectory(dir); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	path := filepath.Join(dir, "config.toml")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	err = privateFile(file)
	if err == nil {
		_, err = file.Write(original)
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	cmd := editorCommand(ctx, path, command)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return &EditError{Path: path, Err: err}
	}
	edited, err := readRegular(path)
	if err != nil {
		return &EditError{Path: path, Err: err}
	}
	if !bytes.Equal(original, edited) {
		if err := f.Save(ctx, edited); err != nil {
			return &EditError{Path: path, Err: err}
		}
	}
	return os.RemoveAll(dir)
}

func editorCommand(ctx context.Context, path string, command []string) *exec.Cmd {
	if len(command) > 0 {
		args := append(append([]string{}, command[1:]...), path)
		return exec.CommandContext(ctx, command[0], args...)
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor != "" {
		if runtime.GOOS == "windows" {
			return exec.CommandContext(ctx, "cmd.exe", "/D", "/S", "/C", editor+` "`+path+`"`)
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", `exec `+editor+` "$1"`, "config-editor", path)
	}
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "notepad.exe", path)
	}
	return exec.CommandContext(ctx, "vi", path)
}
