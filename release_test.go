package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrepareReleasePreservesPublishedBinary(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("first binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := prepareRelease(dir, source, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	wantName := "myappd"
	if runtime.GOOS == "windows" {
		wantName += ".exe"
	}
	if filepath.Base(first) != wantName {
		t.Fatalf("executable = %q; want %q", first, wantName)
	}
	before, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	again, err := prepareRelease(dir, source, "myapp")
	if err != nil || again != first {
		t.Fatalf("reuse = %q, %v", again, err)
	}
	after, err := os.Stat(first)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("existing executable was replaced")
	}
	if err := os.WriteFile(source, []byte("second binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := prepareRelease(dir, source, "myapp")
	if err != nil || first == second {
		t.Fatalf("new release = %q, %v", second, err)
	}
	data, err := os.ReadFile(first)
	if err != nil || string(data) != "first binary" {
		t.Fatalf("previous release changed: %q, %v", data, err)
	}
	if err := os.WriteFile(second, []byte("modified"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRelease(dir, source, "myapp"); err == nil {
		t.Fatal("modified managed executable accepted")
	}
}

func TestReleaseValidationRejectsSymlinkDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires Windows privileges")
	}
	m, _ := testManager(t)
	if err := m.Update(t.Context(), m.executable); err != nil {
		t.Fatal(err)
	}
	selection, err := m.readSelection()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(m.app.directory, "releases", selection.Release)
	saved := directory + "-saved"
	if err := os.Rename(directory, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(saved, directory); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(t.Context(), m.executable); err == nil {
		t.Fatal("Update accepted symlink release directory")
	}
	if err := m.EnsureRunning(t.Context()); err == nil {
		t.Fatal("EnsureRunning accepted symlink release directory")
	}
}
