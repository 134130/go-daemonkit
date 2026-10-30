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
	output, err := execute(t.Context(), NewDaemonCommand(func(*cobra.Command) (*daemon.Manager, error) { return manager, nil }), "status", "--json")
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
	output, err = execute(t.Context(), NewDaemonCommand(func(*cobra.Command) (*daemon.Manager, error) { return manager, nil }), "status")
	if err != nil || !strings.Contains(output, "1.2.3") || !strings.Contains(output, "stopped") {
		t.Fatalf("%q, %v", output, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err = execute(ctx, NewDaemonCommand(func(*cobra.Command) (*daemon.Manager, error) { return manager, nil }), "status", "--json")
	if !errors.Is(err, context.Canceled) || !strings.Contains(output, `"State":"unknown"`) {
		t.Fatalf("%q, %v", output, err)
	}
	for _, name := range []string{"status", "start", "stop", "restart"} {
		if _, err := execute(t.Context(), NewDaemonCommand(func(*cobra.Command) (*daemon.Manager, error) { return manager, nil }), name, "unexpected"); err == nil {
			t.Fatalf("%s accepted extra args", name)
		}
	}
	for _, name := range []string{"start", "stop", "restart"} {
		if _, err := execute(ctx, NewDaemonCommand(func(*cobra.Command) (*daemon.Manager, error) { return manager, nil }), name); !errors.Is(err, context.Canceled) {
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
		{[]string{"validate"}, "Configuration valid\n"},
		{[]string{"set", "--", "count", "-2"}, ""},
		{[]string{"get", "count"}, "-2\n"},
	} {
		output, err := execute(t.Context(), NewConfigCommand(func(*cobra.Command) (*config.File[settings], error) { return file, nil }), tc.args...)
		if err != nil || output != tc.want {
			t.Fatalf("%v = %q, %v", tc.args, output, err)
		}
	}
	want := "# preserved\ncount = -2 # comment\n"
	for _, args := range [][]string{{"set", "count", `"wrong type"`}, {"get"}, {"edit", "extra"}, {"path", "extra"}, {"set", "count"}, {"get", "missing"}, {"reload"}} {
		if _, err := execute(t.Context(), NewConfigCommand(func(*cobra.Command) (*config.File[settings], error) { return file, nil }), args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file = %q, %v", data, err)
	}
	called := false
	group := NewConfigCommand(func(*cobra.Command) (*config.File[settings], error) { return file, nil })
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
	cmd := NewConfigCommand(func(*cobra.Command) (*config.File[struct{}], error) { return file, nil })
	cmd.SetOut(failingWriter{})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"path"})
	if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestResolversRunAfterPreRun(t *testing.T) {
	failure := errors.New("resolver failed")
	for _, group := range []string{"daemon", "config"} {
		operations := [][]string{{"status"}, {"start"}, {"stop"}, {"restart"}}
		if group == "config" {
			operations = [][]string{{"path"}, {"get", "key"}, {"set", "key", "1"}, {"edit"}, {"validate"}}
		}
		for _, args := range append(operations, []string{"--help"}, []string{"invalid"}, []string{operations[0][0], "extra"}) {
			t.Run(group+"/"+strings.Join(args, "_"), func(t *testing.T) {
				var selected string
				calls := 0
				prepared := false
				check := func(cmd *cobra.Command) {
					calls++
					if !prepared || selected != "chosen" {
						t.Fatalf("resolver ran before flags/hooks")
					}
					if cmd.Context() != t.Context() {
						t.Fatal("context not propagated")
					}
				}
				var child *cobra.Command
				if group == "daemon" {
					child = NewDaemonCommand(func(cmd *cobra.Command) (*daemon.Manager, error) { check(cmd); return nil, failure })
				} else {
					child = NewConfigCommand(func(cmd *cobra.Command) (*config.File[struct{}], error) { check(cmd); return nil, failure })
				}
				root := &cobra.Command{Use: "test", PersistentPreRunE: func(*cobra.Command, []string) error { prepared = true; return nil }}
				root.PersistentFlags().StringVar(&selected, "selection", "", "selection")
				root.AddCommand(child)
				_, err := execute(t.Context(), root, append([]string{"--selection", "chosen", group}, args...)...)
				skipped := args[0] == "--help" || args[0] == "invalid" || args[len(args)-1] == "extra"
				if skipped {
					if calls != 0 {
						t.Fatalf("resolver called %d times", calls)
					}
				} else if calls != 1 || !errors.Is(err, failure) {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			})
		}
	}
}

func TestConfigValidateAndMalformedPath(t *testing.T) {
	type settings struct {
		Count int `toml:"count"`
	}
	invalid := errors.New("negative count")
	path := filepath.Join(t.TempDir(), "config.toml")
	resolve := func(*cobra.Command) (*config.File[settings], error) {
		return config.New(config.Options[settings]{Path: path, Validate: func(_ context.Context, s settings) error {
			if s.Count < 0 {
				return invalid
			}
			return nil
		}})
	}
	if _, err := execute(t.Context(), NewConfigCommand(resolve), "validate"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing explicit file: %v", err)
	}
	for _, data := range []string{"count = [", "count = -1"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t.Context(), NewConfigCommand(resolve), "validate"); err == nil {
			t.Fatalf("accepted %q", data)
		}
		output, err := execute(t.Context(), NewConfigCommand(resolve), "path")
		if err != nil || output != path+"\n" {
			t.Fatalf("path blocked by invalid TOML: %q %v", output, err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	defaults := func(*cobra.Command) (*config.File[settings], error) {
		return config.New(config.Options[settings]{AppID: "cobra-config", Defaults: func() settings { return settings{Count: 1} }})
	}
	if output, err := execute(t.Context(), NewConfigCommand(defaults), "validate"); err != nil || output != "Configuration valid\n" {
		t.Fatalf("default validation: %q %v", output, err)
	}
}

func TestResolverNilAndPreRunFailure(t *testing.T) {
	calls := 0
	group := NewDaemonCommand(func(*cobra.Command) (*daemon.Manager, error) { calls++; return nil, nil })
	if _, err := execute(t.Context(), group, "status"); err == nil || calls != 1 {
		t.Fatalf("nil result: %v", err)
	}
	failure := errors.New("pre-run failed")
	root := &cobra.Command{Use: "test", PersistentPreRunE: func(*cobra.Command, []string) error { return failure }}
	root.AddCommand(group)
	if _, err := execute(t.Context(), root, "daemon", "status"); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("pre-run failure: %v, calls=%d", err, calls)
	}
}
