package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
	"github.com/charmbracelet/x/ansi"
)

func TestLayouts(t *testing.T) {
	for _, size := range []Size{{100, 30}, {60, 40}, {20, 10}, {1, 1}, {0, 0}} {
		m := newModel(context.Background(), &session{options: daemon.Options{Version: "0.0.1"}}, []build{{version: "0.0.1"}, {version: "0.0.2"}})
		m.Update(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
		m.logs.Update(logEntryMsg("hello"), layout(size, true).log)
		before := m.logs.viewport.YOffset()
		first := m.View().Content
		if m.View().Content != first || m.logs.viewport.YOffset() != before {
			t.Fatal("View mutated state")
		}
		if size.Height > 0 && len(strings.Split(first, "\n")) > size.Height {
			t.Fatalf("%v: too tall", size)
		}
		for _, line := range strings.Split(first, "\n") {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("%v: too wide", size)
			}
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		want := layout(size, false).log
		if m.logs.viewport.Width() != max(0, want.Width-2) || m.logs.viewport.Height() != max(0, want.Height-3) {
			t.Fatal("layout change did not synchronize viewport")
		}
	}
}

func TestClockLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ticks := make(chan api.NowResponse, 2)
		s := ClockService{logger: log.New(io.Discard, "", 0), publish: func(tick api.NowResponse) error { ticks <- tick; return nil }}
		if err := s.Startup(context.Background()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		select {
		case <-ticks:
		default:
			t.Fatal("missing tick")
		}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		select {
		case <-ticks:
			t.Fatal("tick after shutdown")
		default:
		}
	})
}

func TestVersionSwitch(t *testing.T) {
	// Keep Unix socket paths short, including on macOS.
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "dk-demo-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, v := range []string{"0.0.1", "0.0.2", "0.0.3"} {
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+v, "-o", filepath.Join(dir, "update-demo-v"+v+suffix), ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
	}
	builds, err := discoverBuilds(dir)
	if err != nil {
		t.Fatal(err)
	}
	options := daemon.Options{AppID: "daemonkit-demo", Version: "0.0.1"}
	manager, err := daemon.NewManager(options, "daemon", "run")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := &session{manager: manager, options: options}
	defer func() {
		s.close()
		cleanup, end := context.WithTimeout(context.Background(), 20*time.Second)
		defer end()
		if err := manager.Stop(cleanup); err != nil {
			t.Error(err)
		}
	}()
	if err := manager.Update(ctx, builds[0].path); err != nil {
		t.Fatal(err)
	}
	initial := s.execute(ctx, connectAction, "", daemon.UpdateToken{})
	if initial.err != nil {
		t.Fatal(initial.err)
	}
	expected := initial.info.UpdateToken
	var previousPID int
	for _, index := range []int{0, 1, 2, 0} {
		b := builds[index]
		result := s.execute(ctx, updateAction, b.path, expected)
		if result.err != nil {
			t.Fatalf("switch %s: %v", b.version, result.err)
		}
		if !result.connected || result.info.Version != b.version || result.info.PID == previousPID {
			t.Fatalf("unexpected daemon: %+v", result)
		}
		previousPID = result.info.PID
		expected = result.info.UpdateToken
		for _, a := range []action{nowAction, rawAction, greetAction, runtimeAction} {
			result = s.execute(ctx, a, "", daemon.UpdateToken{})
			supported := a == nowAction || a == rawAction || a == greetAction && index >= 1 || a == runtimeAction && index >= 2
			if supported {
				if result.err != nil {
					t.Fatalf("%s on %s: %v", a, b.version, result.err)
				}
			} else {
				var rpcErr *daemon.RPCError
				if !errors.As(result.err, &rpcErr) || rpcErr.Code != -32601 {
					t.Fatalf("%s on %s: want method not found, got %v", a, b.version, result.err)
				}
			}
		}
	}
	// Saving changes disk only; explicit Reload publishes the validated candidate.
	if result := s.execute(ctx, updateAction, builds[1].path, expected); result.err != nil {
		t.Fatal(result.err)
	}
	command := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, builds[0].path, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("config command: %v\n%s", err, out)
		}
		return out
	}
	command("config", "set", "daemon.greeting", `"Welcome"`)
	if got := string(command("config", "get", "daemon.greeting")); !strings.Contains(got, "Welcome") {
		t.Fatal(got)
	}
	before := s.execute(ctx, greetAction, "", daemon.UpdateToken{})
	if before.err != nil || !strings.HasPrefix(before.detail, "Hello,") {
		t.Fatalf("save applied prematurely: %+v", before)
	}
	command("config", "reload")
	after := s.execute(ctx, greetAction, "", daemon.UpdateToken{})
	if after.err != nil || !strings.HasPrefix(after.detail, "Welcome,") {
		t.Fatalf("reload not applied: %+v", after)
	}
	file, err := configFile(options, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Path(), []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, builds[0].path, "config", "reload")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("invalid reload accepted: %s", out)
	}
	after = s.execute(ctx, greetAction, "", daemon.UpdateToken{})
	if after.err != nil || !strings.HasPrefix(after.detail, "Welcome,") {
		t.Fatalf("failed reload changed runtime: %+v", after)
	}
	if err := file.Save(ctx, []byte("[daemon]\ngreeting='Hello'\n")); err != nil {
		t.Fatal(err)
	}
	command("config", "reload")
	state, err := manager.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := s.execute(ctx, updateAction, builds[0].path, state.UpdateToken)
	if restored.err != nil {
		t.Fatal(restored.err)
	}
	expected = restored.info.UpdateToken
	if result := s.execute(ctx, watchAction, "", daemon.UpdateToken{}); result.err != nil {
		t.Fatal(result.err)
	}
	if result := s.execute(ctx, connectAction, "", daemon.UpdateToken{}); result.err != nil || result.info.Version != "0.0.1" {
		t.Fatalf("reconnect: %+v", result)
	}
	// Missing input must leave the active daemon usable.
	if result := s.execute(ctx, updateAction, filepath.Join(dir, "missing"), expected); result.err == nil || !result.connected || result.info.Version != "0.0.1" {
		t.Fatalf("failed update: %+v", result)
	}
	beforeStop := s.execute(ctx, nowAction, "", daemon.UpdateToken{})
	if beforeStop.err != nil {
		t.Fatal(beforeStop.err)
	}
	ui := newModel(ctx, s, builds)
	ui.Update(beforeStop)
	observed := make(chan tea.Msg, 1)
	go func() { observed <- watchConnection(ctx, beforeStop.client)() }()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-observed:
		ui.Update(message)
	case <-time.After(5 * time.Second):
		t.Fatal("remote stop was not observed")
	}
	if ui.overview.connected || ui.overview.info.State != daemon.StatusUnknown {
		t.Fatal("closed connection displayed as Ready")
	}
	ui.Update(beforeStop) // A queued response must not resurrect a closed connection.
	if ui.overview.connected {
		t.Fatal("late response restored closed connection")
	}
	if result := s.execute(ctx, stopAction, "", daemon.UpdateToken{}); result.err != nil || result.connected {
		t.Fatalf("stop: %+v", result)
	}
	if result := s.execute(ctx, updateAction, builds[2].path, expected); !errors.Is(result.err, daemon.ErrUpdateConflict) || result.connected {
		t.Fatalf("stopped conflict: %+v", result)
	}
	restarted := s.execute(ctx, connectAction, "", daemon.UpdateToken{})
	if restarted.err != nil {
		t.Fatal(restarted.err)
	}
	if result := s.execute(ctx, updateAction, builds[2].path, expected); !errors.Is(result.err, daemon.ErrUpdateConflict) || result.info.Version != "0.0.1" || len(result.builds) != 3 {
		t.Fatalf("stale token: %+v", result)
	}
	if result := s.execute(ctx, updateAction, builds[2].path, restarted.info.UpdateToken); result.err != nil || result.info.Version != "0.0.3" {
		t.Fatalf("fresh selection: %+v", result)
	}
}

func TestDiscoverBuilds(t *testing.T) {
	dir := t.TempDir()
	if _, err := discoverBuilds(dir); err == nil {
		t.Fatal("empty directory accepted")
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	path := filepath.Join(dir, "update-demo-v0.0.2"+suffix)
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	builds, err := discoverBuilds(dir)
	if err != nil || len(builds) != 1 || builds[0].version != "0.0.2" {
		t.Fatalf("%v %v", builds, err)
	}
}

func TestModelIgnoresOldCommandsAndConnections(t *testing.T) {
	m := newModel(context.Background(), &session{}, []build{{version: "0.0.3"}})
	m.epoch = 2
	m.client = &daemon.Client{}
	m.overview.info.Version = "0.0.3"
	m.overview.connected = true
	m.Update(resultMsg{epoch: 1, info: daemon.Status{Version: "0.0.1"}})
	m.Update(disconnectedMsg{client: &daemon.Client{}})
	if !m.busy || !m.overview.connected || m.overview.info.Version != "0.0.3" {
		t.Fatal("stale event changed the UI")
	}
	m.Update(resultMsg{epoch: 2, err: daemon.ErrUpdateConflict, builds: []build{{version: "0.0.2"}}})
	if m.busy || len(m.overview.builds) != 1 || m.overview.builds[0].version != "0.0.2" || !strings.Contains(m.status, "select a build again") {
		t.Fatal("conflict did not refresh selection")
	}
}

func TestConnectionWatchStopsWithUI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan tea.Msg, 1)
		go func() { result <- watchConnection(ctx, &daemon.Client{})() }()
		synctest.Wait()
		cancel()
		if msg := <-result; msg != nil {
			t.Fatalf("canceled watcher: %v", msg)
		}
	})
}
