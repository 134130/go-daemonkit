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

	storageimpl "github.com/134130/go-daemonkit/internal/storage"
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
	serviceMu sync.Mutex
	services  []registeredService
	sealed    bool
	running   bool
	storage   storageimpl.Database
	events    eventHub
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
	a.serviceMu.Lock()
	if a.running {
		a.serviceMu.Unlock()
		return ErrAlreadyRunning
	}
	a.sealed, a.running = true, true
	a.serviceMu.Unlock()
	defer func() { a.serviceMu.Lock(); a.running = false; a.serviceMu.Unlock() }()

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
	if requested := os.Getenv(launchNonceEnv); requested != "" {
		reservation, err := readLaunch(a.directory)
		if err != nil {
			return err
		}
		if reservation.LaunchNonce != requested {
			return fmt.Errorf("launch reservation does not match requested generation")
		}
		nonce = requested
	}
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
	if err := a.openStorage(ctx); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, a.closeStorage()) }()
	a.events.start()
	defer a.events.close()
	started, startupErr := a.startServices(ctx)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
		defer cancel()
		err = errors.Join(err, shutdownServices(cleanupCtx, started))
	}()
	if startupErr != nil {
		return startupErr
	}
	if err := publish(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	identity := initializeResult{a.options.AppID, a.options.InstanceID, protocolVersion, a.options.Version, record.LaunchNonce}
	rpc := newRPCServer(a.services)
	rpc.events = &a.events
	var mu sync.Mutex
	var handlers sync.WaitGroup
	active := 0
	server := &http.Server{
		ReadHeaderTimeout: handshakeTimeout,
		WriteTimeout:      handshakeTimeout,
		MaxHeaderBytes:    16 << 10,
	}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rpc.mu.Lock()
		stopping := rpc.stopping
		rpc.mu.Unlock()
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
		rpc.serve(runCtx, conn)
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	var serveErr error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-rpc.stop:
	case serveErr = <-served:
		served = nil
	}
	rpc.mu.Lock()
	rpc.stopping = true
	rpc.mu.Unlock()
	closeErr := server.Close()
	if served != nil {
		serveErr = <-served
	}
	// Admission is closed before Wait, so no new calls can be added.
	drained := make(chan struct{})
	go func() { rpc.calls.Wait(); close(drained) }()
	timer := time.NewTimer(stopGrace - time.Second)
	select {
	case <-drained:
	case <-timer.C:
	}
	timer.Stop()
	cancel()
	// Give cooperative cancellation time to unwind before Service cleanup.
	timer = time.NewTimer(time.Second)
	select {
	case <-drained:
	case <-timer.C:
		err = errors.Join(err, fmt.Errorf("RPC shutdown deadline exceeded"))
	}
	timer.Stop()
	handlers.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(err, closeErr, serveErr)
}

func replyRPCError(ctx context.Context, conn *websocket.Conn, id json.RawMessage, failure *RPCError) error {
	replyCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return writeRPC(replyCtx, conn, rpcMessage{JSONRPC: "2.0", ID: id, Error: failure})
}
