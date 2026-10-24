package daemon

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	logQueueCapacity = 256
	maxLogWrite      = 64 << 10
	logFileSize      = 10 << 20
	logBackups       = 3
	logStopGrace     = 2 * time.Second
	rawLogInterval   = 30 * time.Second
	rawLogTail       = 1 << 20
)

// ErrLogShutdownTimeout means logging cleanup still owns this execution's
// runtime lease. A managed daemon should exit after Run returns.
var ErrLogShutdownTimeout = errors.New("log cleanup deadline exceeded; runtime ownership retained")

// LogWriter returns an App-owned asynchronous writer, usable from Startup through
// Shutdown. Writes never wait for disk I/O or queue space. Each Write is copied
// as one opaque record (at most 64 KiB); at most 256 records may be outstanding.
// Oversized writes and writes to a full queue are dropped, returning len(p), nil.
// Writes outside Run return os.ErrClosed. Acceptance is not a durability promise.
// Callers own flushing any buffering they add before their Shutdown returns.
func (a *App) LogWriter() io.Writer { return &a.logs }

type logWriter struct {
	mu       sync.Mutex
	active   *logSession
	dropped  atomic.Uint64
	failures atomic.Uint64
}

type logRecord struct {
	data       []byte
	diagnostic bool
}

type logSession struct {
	queue      chan logRecord
	slots      chan struct{}
	done       chan struct{}
	abandon    atomic.Bool
	draining   atomic.Bool
	sink       io.WriteCloser
	diagnostic io.WriteCloser
	rawPaths   []string
	closeErr   error // read after done
}

func (w *logWriter) Write(p []byte) (int, error) { return w.write(p, false) }

func (w *logWriter) write(p []byte, diagnostic bool) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.active
	if s == nil {
		return 0, os.ErrClosed
	}
	if len(p) > maxLogWrite {
		w.dropped.Add(1)
		return len(p), nil
	}
	// Reserve before allocating; slots include copying, queued and in-flight data.
	select {
	case s.slots <- struct{}{}:
	default:
		w.dropped.Add(1)
		return len(p), nil
	}
	s.queue <- logRecord{bytes.Clone(p), diagnostic}
	return len(p), nil
}

type diagnosticLogWriter struct{ logs *logWriter }

func (w diagnosticLogWriter) Write(p []byte) (int, error) { return w.logs.write(p, true) }

func (w *logWriter) start(sink, diagnostic io.WriteCloser, rawPaths []string) *logSession {
	s := &logSession{
		queue: make(chan logRecord, logQueueCapacity), slots: make(chan struct{}, logQueueCapacity),
		done: make(chan struct{}), sink: sink, diagnostic: diagnostic, rawPaths: rawPaths,
	}
	w.mu.Lock()
	w.active = s
	w.mu.Unlock()
	go w.work(s)
	return s
}

func (w *logWriter) stop(s *logSession) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active == s {
		w.active = nil
		s.draining.Store(true)
		close(s.queue)
	}
}

func (w *logWriter) work(s *logSession) {
	defer close(s.done)
	defer func() {
		s.closeErr = s.sink.Close()
		if s.diagnostic != nil {
			s.closeErr = errors.Join(s.closeErr, s.diagnostic.Close())
		}
	}()
	ticker := time.NewTicker(rawLogInterval)
	defer ticker.Stop()
	for {
		select {
		case r, ok := <-s.queue:
			if !ok {
				return
			}
			if s.abandon.Load() {
				w.dropped.Add(1)
			} else {
				target := io.Writer(s.sink)
				if r.diagnostic && s.diagnostic != nil {
					target = s.diagnostic
				}
				n, err := target.Write(r.data)
				if err != nil || n != len(r.data) {
					w.failures.Add(1)
					w.dropped.Add(1)
				}
			}
			<-s.slots
		case <-ticker.C:
			if s.draining.Load() || s.abandon.Load() {
				continue
			}
			for _, path := range s.rawPaths {
				if s.draining.Load() || s.abandon.Load() {
					break
				}
				if err := pruneRawLog(path); err != nil {
					w.failures.Add(1)
				}
			}
		}
	}
}

func (a *App) openLogs() (*logSession, error) {
	dir, err := prepareLogDirectory(a.directory)
	if err != nil {
		return nil, err
	}
	sink, err := newRotatingLog(filepath.Join(dir, "daemon.log"))
	if err != nil {
		return nil, err
	}
	diagnostic, err := openLogFile(filepath.Join(dir, "daemon.stderr"), os.O_CREATE|os.O_APPEND|os.O_RDWR)
	if err != nil {
		return nil, errors.Join(err, sink.Close())
	}
	return a.logs.start(sink, diagnostic, []string{
		filepath.Join(dir, "daemon.stdout"), filepath.Join(dir, "daemon.stderr"),
	}), nil
}
