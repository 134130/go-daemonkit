package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareReleasePreservesPublishedBinary(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("first binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := prepareRelease(dir, source)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	again, err := prepareRelease(dir, source)
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
	second, err := prepareRelease(dir, source)
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
	if _, err := prepareRelease(dir, source); err == nil {
		t.Fatal("modified managed executable accepted")
	}
}
