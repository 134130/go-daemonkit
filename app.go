package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrAlreadyRunning means another execution holds this instance's runtime lock.
var ErrAlreadyRunning = errors.New("daemon instance is already running")

// Options identifies an application and one of its user-local instances.
type Options struct {
	AppID string
	// InstanceID defaults to "default".
	InstanceID string
	// Version identifies the binary for diagnostics, not protocol compatibility.
	Version string
}

// App runs a daemon in the current process. It does not spawn a child process.
type App struct {
	options   Options
	directory string
	endpoint  string
}

// New validates configuration and resolves paths without creating resources.
func New(options Options) (*App, error) {
	if options.InstanceID == "" {
		options.InstanceID = "default"
	}
	if !validName(options.AppID) || !validName(options.InstanceID) || strings.TrimSpace(options.Version) == "" {
		return nil, fmt.Errorf("appID and instanceID must be path-safe identifiers; version is required")
	}
	root, err := stateRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, options.AppID, options.InstanceID)
	endpoint, err := instanceEndpoint(dir)
	if err != nil {
		return nil, err
	}
	return &App{options: options, directory: dir, endpoint: endpoint}, nil
}

func validName(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." || !filepath.IsLocal(name) {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func stateRoot() (string, error) {
	var root string
	switch runtime.GOOS {
	case "linux", "darwin":
		root = os.Getenv("XDG_STATE_HOME")
		if !filepath.IsAbs(root) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			root = filepath.Join(home, ".local", "state")
		}
	case "windows":
		root = os.Getenv("LOCALAPPDATA")
	default:
		return "", fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("state directory must be absolute")
	}
	return filepath.Clean(root), nil
}

// Run blocks until shutdown, startup failure, or ctx cancellation. It retains
// the instance lock until all connections and the listener have been closed.
// A shutdown RPC returns nil; caller cancellation returns ctx.Err().
func (a *App) Run(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockInstance(a.directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := recoverExecution(a.directory, a.endpoint); err != nil {
		return err
	}
	process, err := processIdentity(os.Getpid())
	if err != nil {
		return err
	}
	nonce := rand.Text()
	listener, socket, publish, err := prepareExecutionEndpoint(a.endpoint, nonce)
	if err != nil {
		return err
	}
	defer listener.Close()
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: process, LaunchNonce: nonce, SocketIdentity: socket}
	if err := writeExecution(a.directory, record); err != nil {
		return err
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
			return
		}
		err = errors.Join(err, removeExecution(a.directory, record))
	}()
	if err := publish(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	identity := initializeResult{a.options.AppID, a.options.InstanceID, protocolVersion, a.options.Version, record.LaunchNonce}
	stop := make(chan struct{})
	var mu sync.Mutex
	var handlers sync.WaitGroup
	stopping := false
	active := 0
	server := &http.Server{
		ReadHeaderTimeout: handshakeTimeout,
		WriteTimeout:      handshakeTimeout,
		MaxHeaderBytes:    16 << 10,
	}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if stopping || active >= 128 {
			mu.Unlock()
			http.Error(w, "daemon unavailable", http.StatusServiceUnavailable)
			return
		}
		active++
		handlers.Add(1)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock(); handlers.Done() }()
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if initializeServer(runCtx, conn, identity) != nil {
			return
		}
		for {
			request, err := readRPC(runCtx, conn)
			if err != nil {
				_ = replyReadError(runCtx, conn, err)
				return
			}
			if request.Method == "initialize" || request.Method == "initialized" || request.Method == "" || request.Error != nil || len(request.Result) != 0 || len(request.ID) != 0 && !validRequestID(request.ID) {
				_ = rejectHandshake(runCtx, conn, request, -32600, "invalid request or initialization sequence")
				return
			}
			// Notifications never initiate shutdown and never receive a response.
			if len(request.ID) == 0 {
				continue
			}
			if request.Method != "shutdown" {
				if replyControlError(runCtx, conn, request.ID, -32601, "method not found") != nil {
					return
				}
				continue
			}
			if !emptyParams(request.Params) {
				if replyControlError(runCtx, conn, request.ID, -32602, "shutdown takes no parameters") != nil {
					return
				}
				continue
			}
			mu.Lock()
			if stopping {
				mu.Unlock()
				return
			}
			stopping = true
			mu.Unlock()
			// Shutdown continues even if the caller disappears during the reply.
			defer close(stop)
			replyCtx, replyCancel := context.WithTimeout(runCtx, time.Second)
			_ = writeRPC(replyCtx, conn, rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`null`)})
			replyCancel()
			return
		}
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	var serveErr error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-stop:
	case serveErr = <-served:
		served = nil
	}
	mu.Lock()
	stopping = true
	mu.Unlock()
	cancel()
	closeErr := server.Close()
	if served != nil {
		serveErr = <-served
	}
	handlers.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(err, closeErr, serveErr)
}

func replyControlError(ctx context.Context, conn *websocket.Conn, id json.RawMessage, code int, message string) error {
	replyCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return writeRPC(replyCtx, conn, rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{code, message}})
}
