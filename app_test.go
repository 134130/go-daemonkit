package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		root = filepath.Dir(testLocalPath(t))
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	app, err := New(Options{AppID: "test", Version: "test-binary"})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func runTestApp(t *testing.T, app *App) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("app did not stop during cleanup")
		}
	})
	return cancel, done
}

func connectTestApp(t *testing.T, app *App) (*websocket.Conn, initializeResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	params := initializeParams{app.options.AppID, app.options.InstanceID, protocolVersion, "test-client"}
	for {
		conn, identity, err := connectLocal(ctx, app.endpoint, params)
		if err == nil {
			t.Cleanup(func() { _ = conn.CloseNow() })
			return conn, identity
		}
		select {
		case <-ctx.Done():
			t.Fatalf("app did not become ready: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAppShutdownAndRestart(t *testing.T) {
	app := testApp(t)
	var previous string
	for range 2 {
		_, done := runTestApp(t, app)
		conn, identity := connectTestApp(t, app)
		if identity.LaunchNonce == "" || identity.LaunchNonce == previous {
			t.Fatal("execution nonce was reused")
		}
		previous = identity.LaunchNonce
		if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "shutdown"}); err != nil {
			t.Fatal(err)
		}
		response, err := readRPC(t.Context(), conn)
		if err != nil || response.Error != nil || string(response.Result) != "null" {
			t.Fatalf("shutdown response = %+v, %v", response, err)
		}
		if err := awaitApp(t, done); err != nil {
			t.Fatal(err)
		}
	}
}

func awaitApp(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("app did not exit")
		return nil
	}
}

func TestAppCancellationClosesConnections(t *testing.T) {
	app := testApp(t)
	cancel, done := runTestApp(t, app)
	conn, _ := connectTestApp(t, app)
	cancel()
	if err := awaitApp(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if _, err := readRPC(ctx, conn); err == nil {
		t.Fatal("connection survived app cancellation")
	}
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatalf("lock retained after cancellation: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestAppRejectsDuplicateProcess(t *testing.T) {
	app := testApp(t)
	_, _ = runTestApp(t, app)
	_, _ = connectTestApp(t, app)
	if err := app.Run(t.Context()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate Run = %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestAppLockHelper$")
	command.Env = append(os.Environ(), "DAEMONKIT_LOCK_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock failed: %v\n%s", err, output)
	}
}

func TestAppLockHelper(t *testing.T) {
	if os.Getenv("DAEMONKIT_LOCK_HELPER") != "1" {
		return
	}
	app, err := New(Options{AppID: "test", Version: "child"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := app.Run(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("child Run = %v; want instance lock rejection", err)
	}
}

func TestAppRejectsInvalidOptions(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../app", "a/b", "a\\b"} {
		if _, err := New(Options{AppID: id, Version: "v1"}); err == nil {
			t.Fatalf("invalid app ID accepted: %q", id)
		}
	}
	if _, err := New(Options{AppID: "app"}); err == nil {
		t.Fatal("missing version accepted")
	}
}

func TestNewHasNoFilesystemSideEffects(t *testing.T) {
	app := testApp(t)
	root, err := stateRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "test", "default"); app.directory != want {
		t.Fatalf("instance directory = %q; want %q", app.directory, want)
	}
	if _, err := os.Stat(app.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New created instance directory: %v", err)
	}
	other, err := New(Options{AppID: app.options.AppID, Version: "new-binary"})
	if err != nil {
		t.Fatal(err)
	}
	if app.endpoint != other.endpoint || app.directory != other.directory {
		t.Fatal("binary version changed instance paths")
	}
}

func TestUnixStateRoot(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("XDG state paths apply to Linux and Darwin")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct{ name, value, want string }{
		{"explicit", filepath.Join(home, "custom-state"), filepath.Join(home, "custom-state")},
		{"empty", "", filepath.Join(home, ".local", "state")},
		{"relative", "relative-state", filepath.Join(home, ".local", "state")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", tc.value)
			got, err := stateRoot()
			if err != nil || got != tc.want {
				t.Fatalf("stateRoot = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestAppStartupFailureReleasesLock(t *testing.T) {
	app := testApp(t)
	// An invalid endpoint fails after the instance lock has been acquired.
	app.endpoint = "invalid-local-endpoint"
	if err := app.Run(t.Context()); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatalf("startup failure retained lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestAppInvalidShutdownDoesNotStopDaemon(t *testing.T) {
	app := testApp(t)
	_, done := runTestApp(t, app)
	conn, _ := connectTestApp(t, app)
	for _, request := range []rpcMessage{
		{JSONRPC: "2.0", Method: "shutdown"},
		{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "shutdown", Params: json.RawMessage(`{"unexpected":true}`)},
	} {
		if err := writeRPC(t.Context(), conn, request); err != nil {
			t.Fatal(err)
		}
	}
	response, err := readRPC(t.Context(), conn)
	if err != nil || string(response.ID) != "3" || response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("invalid shutdown response = %+v, %v", response, err)
	}
	select {
	case err := <-done:
		t.Fatalf("invalid shutdown stopped app: %v", err)
	default:
	}
	// A completed second handshake proves that the admission gate remains open.
	_, _ = connectTestApp(t, app)
}
