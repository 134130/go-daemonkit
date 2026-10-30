package daemoncobra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/config"
	"github.com/spf13/cobra"
)

func execute(ctx context.Context, command *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(io.Discard)
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetArgs(args)
	err := command.ExecuteContext(ctx)
	return out.String(), err
}

func TestDaemonStatus(t *testing.T) {
	dir, err := os.MkdirTemp("", "dc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("LOCALAPPDATA", dir)
	manager, err := daemon.NewManager(daemon.Options{AppID: "cobra-test", Version: "1.2.3"}, "run")
	if err != nil {
		t.Fatal(err)
	}
	output, err := execute(t.Context(), NewDaemonCommand(manager), "status", "--json")
	var status daemon.Status
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != daemon.StatusStopped || status.ClientVersion != "1.2.3" {
		t.Fatalf("status: %+v", status)
	}
	if _, err := os.Stat(status.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created state: %v", err)
	}
	output, err = execute(t.Context(), NewDaemonCommand(manager), "status")
	if err != nil || !strings.Contains(output, "1.2.3") || !strings.Contains(output, "stopped") {
		t.Fatalf("%q, %v", output, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err = execute(ctx, NewDaemonCommand(manager), "status", "--json")
	if !errors.Is(err, context.Canceled) || !strings.Contains(output, `"State":"unknown"`) {
		t.Fatalf("%q, %v", output, err)
	}
	for _, name := range []string{"status", "start", "stop", "restart"} {
		if _, err := execute(t.Context(), NewDaemonCommand(manager), name, "unexpected"); err == nil {
			t.Fatalf("%s accepted extra args", name)
		}
	}
	for _, name := range []string{"start", "stop", "restart"} {
		if _, err := execute(ctx, NewDaemonCommand(manager), name); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestConfigCommands(t *testing.T) {
	type settings struct {
		Count int `toml:"count"`
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# preserved\ncount = 1 # comment\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := config.New(config.Options[settings]{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"path"}, path + "\n"},
		{[]string{"get", "count"}, "1\n"},
		{[]string{"set", "--", "count", "-2"}, ""},
		{[]string{"get", "count"}, "-2\n"},
	} {
		output, err := execute(t.Context(), NewConfigCommand(file), tc.args...)
		if err != nil || output != tc.want {
			t.Fatalf("%v = %q, %v", tc.args, output, err)
		}
	}
	want := "# preserved\ncount = -2 # comment\n"
	for _, args := range [][]string{{"set", "count", `"wrong type"`}, {"get"}, {"edit", "extra"}, {"path", "extra"}, {"set", "count"}, {"get", "missing"}, {"reload"}} {
		if _, err := execute(t.Context(), NewConfigCommand(file), args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file = %q, %v", data, err)
	}
	called := false
	group := NewConfigCommand(file)
	group.AddCommand(&cobra.Command{Use: "reload", RunE: func(*cobra.Command, []string) error { called = true; return nil }})
	if _, err := execute(t.Context(), group, "reload"); err != nil || !called {
		t.Fatalf("reload: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOutputError(t *testing.T) {
	file, err := config.New(config.Options[struct{}]{Path: filepath.Join(t.TempDir(), "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewConfigCommand(file)
	cmd.SetOut(failingWriter{})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"path"})
	if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
