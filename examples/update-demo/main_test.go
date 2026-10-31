package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	daemon "github.com/134130/go-daemonkit"
)

func TestCommandGroups(t *testing.T) {
	dir, err := os.MkdirTemp("", "dc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	path := filepath.Join(dir, "explicit.toml")
	if err := os.WriteFile(path, []byte("broken = ["), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		err := root.ExecuteContext(t.Context())
		return out.String(), err
	}
	for _, args := range [][]string{{"--help"}, {"--version"}, {"config", "path", "--config", path}, {"daemon", "status", "--config", path, "--instance", "cli", "--json"}} {
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		if args[0] == "daemon" {
			var status daemon.Status
			if err := json.Unmarshal([]byte(out), &status); err != nil {
				t.Fatal(err)
			}
			if status.State != daemon.StatusStopped || status.InstanceID != "cli" || status.ClientVersion != version {
				t.Fatalf("%+v", status)
			}
		}
	}
	if _, err := run("config", "validate", "--config", path); err == nil {
		t.Fatal("invalid config accepted")
	}
	if out, err := run("config", "validate"); err != nil || !strings.Contains(out, "valid") {
		t.Fatalf("defaults: %s %v", out, err)
	}
	if err := os.WriteFile(path, []byte("[daemon]\ngreeting = \"Hello\" # retained\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("config", "set", "daemon.greeting", `"Hi"`, "--config", path); err != nil {
		t.Fatal(err)
	}
	if out, err := run("config", "get", "daemon.greeting", "--config", path); err != nil || out != "\"Hi\"\n" {
		t.Fatalf("get: %s %v", out, err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "# retained") {
		t.Fatalf("comment: %s %v", data, err)
	}
}
