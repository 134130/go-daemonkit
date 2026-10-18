package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
		if err := os.Rename(staging, destination); err != nil {
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
	if err := writeJSONRecord(directory, "current.json", struct {
		Release string `json:"release"`
	}{release}); err != nil {
		return "", err
	}
	return executable, nil
}
