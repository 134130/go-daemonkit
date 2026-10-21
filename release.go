package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// prepareRelease never overwrites a published executable, including on Windows.
// The operation lock serializes selection of releases for this instance.
func prepareRelease(directory, source, appID string) (string, error) {
	releases := filepath.Join(directory, "releases")
	if err := os.MkdirAll(releases, 0o700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(releases, ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	name := appID + "d"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("daemon executable must be a regular file")
	}
	output, err := os.OpenFile(filepath.Join(staging, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	defer output.Close()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hash), input); err != nil {
		return "", err
	}
	if err := output.Sync(); err != nil {
		return "", err
	}
	if err := output.Chmod(0o700); err != nil {
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	release := hex.EncodeToString(hash.Sum(nil))
	destination := filepath.Join(releases, release)
	executable := filepath.Join(destination, name)
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		if err := renameFile(staging, destination); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		info, err := os.Lstat(executable)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("managed executable is not a regular file")
		}
		file, err := os.Open(executable)
		if err != nil {
			return "", err
		}
		defer file.Close()
		existingHash := sha256.New()
		if _, err := io.Copy(existingHash, file); err != nil {
			return "", err
		}
		if hex.EncodeToString(existingHash.Sum(nil)) != release {
			return "", fmt.Errorf("managed release content changed")
		}
	}
	return executable, nil
}

// releaseSelection preserves the invocation independently of the next CLI's arguments.
type releaseSelection struct {
	Release string   `json:"release"`
	Args    []string `json:"args"`
}

func (s releaseSelection) valid() bool {
	decoded, err := hex.DecodeString(s.Release)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(s.Release) != s.Release || len(s.Args) == 0 {
		return false
	}
	size := 0
	for _, arg := range s.Args {
		if strings.ContainsRune(arg, 0) {
			return false
		}
		size += len(arg)
	}
	return size <= 4096
}

func (s releaseSelection) same(other releaseSelection) bool {
	return s.Release == other.Release && slices.Equal(s.Args, other.Args)
}

func (m *Manager) readSelection() (releaseSelection, error) {
	var selection releaseSelection
	err := readJSONRecord(m.app.directory, "current.json", &selection)
	if err == nil && !selection.valid() {
		err = fmt.Errorf("invalid release selection")
	}
	return selection, err
}

func (m *Manager) prepareSelection(source string) (releaseSelection, error) {
	path, err := prepareRelease(m.app.directory, source, m.app.options.AppID)
	if err != nil {
		return releaseSelection{}, err
	}
	selection := releaseSelection{Release: filepath.Base(filepath.Dir(path)), Args: slices.Clone(m.args)}
	if !selection.valid() {
		return releaseSelection{}, fmt.Errorf("invalid daemon invocation")
	}
	return selection, nil
}

func (m *Manager) releasePath(selection releaseSelection) (string, error) {
	if !selection.valid() {
		return "", fmt.Errorf("invalid release selection")
	}
	dir := filepath.Join(m.app.directory, "releases", selection.Release)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid release directory")
	}
	name := m.app.options.AppID + "d"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	info, err = os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("invalid release executable")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	if hex.EncodeToString(hash.Sum(nil)) != selection.Release {
		return "", fmt.Errorf("managed release content changed")
	}
	return path, nil
}
