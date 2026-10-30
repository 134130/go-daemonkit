package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigEditorHelper(t *testing.T) {
	mode := os.Getenv("DAEMONKIT_CONFIG_EDITOR")
	if mode == "" {
		return
	}
	path := os.Args[len(os.Args)-1]
	if mode == "noop" {
		return
	}
	if mode == "fail" {
		os.Exit(2)
	}
	data := "[daemon]\ntimeout='edited' # retained\n"
	if mode == "invalid" {
		data = "[daemon]\ntimeout='bad'\n"
	}
	if target := os.Getenv("DAEMONKIT_CONFIG_TARGET"); target != "" {
		if err := os.WriteFile(target, []byte("[daemon]\ntimeout='external'\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEditLastSaveWinsAndRecovery(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "invalid", "noop", "fail"} {
		t.Run(mode, func(t *testing.T) {
			f := testFile(t, "[daemon]\ntimeout='initial'\n")
			t.Setenv("DAEMONKIT_CONFIG_EDITOR", mode)
			if mode == "valid" {
				t.Setenv("DAEMONKIT_CONFIG_TARGET", f.Path())
			}
			err := f.Edit(t.Context(), executable, "-test.run=^TestConfigEditorHelper$", "--")
			var failure *EditError
			if mode == "invalid" || mode == "fail" {
				if !errors.As(err, &failure) {
					t.Fatalf("expected recovery path: %v", err)
				}
				if _, err := os.Stat(failure.Path); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(failure.Path)) })
			} else if err != nil {
				t.Fatal(err)
			}
			got, err := f.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := "initial"
			if mode == "valid" {
				want = "edited"
			}
			if got.Daemon.Timeout != want {
				t.Fatalf("got %s, want %s", got.Daemon.Timeout, want)
			}
		})
	}
}
