package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"golang.org/x/sys/windows"
)

func TestRenameWaitsForOpenHandle(t *testing.T) {
	for _, heldName := range []string{"source", "destination"} {
		t.Run(heldName, func(t *testing.T) {
			for name, release := range map[string]bool{"released": true, "held": false} {
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					source, destination := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
					if err := os.WriteFile(source, []byte("record"), 0o600); err != nil {
						t.Fatal(err)
					}
					heldPath := filepath.Join(dir, heldName)
					if heldName == "destination" {
						if err := os.WriteFile(destination, []byte("previous"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					name, err := windows.UTF16PtrFromString(heldPath)
					if err != nil {
						t.Fatal(err)
					}
					handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if handle != 0 {
							_ = windows.CloseHandle(handle)
						}
					}()
					if err := os.Rename(source, destination); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
						t.Fatalf("rename with open handle: %v", err)
					}
					synctest.Test(t, func(t *testing.T) {
						done := make(chan error, 1)
						go func() { done <- renameFile(source, destination) }()
						synctest.Wait()
						select {
						case err := <-done:
							t.Fatalf("rename returned before retry: %v", err)
						default:
						}
						if release {
							if err := windows.CloseHandle(handle); err != nil {
								t.Fatal(err)
							}
							handle = 0
						}
						err := <-done
						if release && err != nil {
							t.Fatal(err)
						}
						if !release && (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) {
							t.Fatalf("held handle: %v", err)
						}
					})
				})
			}
		})
	}
}
