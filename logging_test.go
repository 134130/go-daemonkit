package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type testLogSink struct {
	bytes.Buffer
	gate      chan struct{}
	closeGate chan struct{}
	fail      bool
	closeErr  error
}

func (s *testLogSink) Write(p []byte) (int, error) {
	if s.gate != nil {
		<-s.gate
	}
	if s.fail {
		s.fail = false
		return 0, io.ErrClosedPipe
	}
	return s.Buffer.Write(p)
}
func (s *testLogSink) Close() error {
	if s.closeGate != nil {
		<-s.closeGate
	}
	return s.closeErr
}

func TestLogWriterBoundsCopyAndDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var w logWriter
		if _, err := w.Write([]byte("before")); !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
		sink := &testLogSink{gate: make(chan struct{})}
		s := w.start(sink, nil, nil)
		input := []byte("original")
		if _, err := w.Write(input); err != nil {
			t.Fatal(err)
		}
		input[0] = 'X'
		synctest.Wait()
		var callers sync.WaitGroup
		for range 1024 {
			callers.Go(func() {
				if n, err := w.Write([]byte("!")); n != 1 || err != nil {
					t.Errorf("Write = %d, %v", n, err)
				}
			})
		}
		callers.Wait()
		if len(s.slots) != logQueueCapacity || len(s.queue) != logQueueCapacity-1 {
			t.Fatalf("unbounded: %d outstanding, %d queued", len(s.slots), len(s.queue))
		}
		if w.dropped.Load() != 1024-(logQueueCapacity-1) {
			t.Fatal("incorrect overflow accounting")
		}
		oversized := make([]byte, maxLogWrite+1)
		if n, err := w.Write(oversized); n != len(oversized) || err != nil {
			t.Fatalf("oversized: %d %v", n, err)
		}
		w.stop(s)
		if _, err := w.Write(nil); !errors.Is(err, os.ErrClosed) {
			t.Fatal("writer still active")
		}
		close(sink.gate)
		<-s.done
		if got := sink.String(); got != "original"+strings.Repeat("!", logQueueCapacity-1) {
			t.Fatalf("copy/order/drain: %q", got)
		}
		if len(s.slots) != 0 {
			t.Fatal("outstanding reservations leaked")
		}
	})
}

func TestLogWriteFailureDoesNotReplayOrStopWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var w logWriter
		sink := &testLogSink{fail: true, closeErr: io.ErrClosedPipe}
		s := w.start(sink, nil, nil)
		_, _ = w.Write([]byte("lost"))
		_, _ = w.Write([]byte("kept"))
		w.stop(s)
		<-s.done
		if sink.String() != "kept" || w.failures.Load() != 1 || w.dropped.Load() != 1 || !errors.Is(s.closeErr, io.ErrClosedPipe) {
			t.Fatal("failure contract violated")
		}
	})
}

func TestLogCleanupTimeoutRetainsOwnership(t *testing.T) {
	for _, blockClose := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "close"}[blockClose], func(t *testing.T) {
			app := testApp(t)
			unlock, err := lockInstance(app.directory)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := processIdentity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			record := executionRecord{PID: os.Getpid(), ProcessIdentity: identity, LaunchNonce: "logging-test-generation"}
			if err := writeExecution(app.directory, record); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				gate := make(chan struct{})
				sink := &testLogSink{}
				if blockClose {
					sink.closeGate = gate
				} else {
					sink.gate = gate
				}
				app.running = true
				s := app.logs.start(sink, nil, nil)
				_, _ = app.LogWriter().Write([]byte("first"))
				_, _ = app.LogWriter().Write([]byte("queued"))
				synctest.Wait()
				start := time.Now()
				if err := app.finishRun(unlock, nil, &record, s); !errors.Is(err, ErrLogShutdownTimeout) {
					t.Fatalf("cleanup = %v", err)
				}
				if time.Since(start) != logStopGrace {
					t.Fatal("cleanup did not respect budget")
				}
				current, err := readExecution(app.directory)
				if err != nil || !current.Closing {
					t.Fatalf("cleanup record missing: %+v %v", current, err)
				}
				if err := app.Run(t.Context()); !errors.Is(err, ErrAlreadyRunning) {
					t.Fatalf("same App reused: %v", err)
				}
				if release, err := lockInstance(app.directory); !errors.Is(err, ErrAlreadyRunning) {
					if release != nil {
						_ = release()
					}
					t.Fatalf("runtime lease lost: %v", err)
				}
				// Stop recognizes retained cleanup even though IPC no longer exists.
				ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
				defer cancel()
				manager := &Manager{app: app}
				if err := manager.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Stop did not wait for cleanup process: %v", err)
				}
				close(gate)
				synctest.Wait()
				if app.running {
					t.Fatal("App stayed active after cleanup")
				}
				if _, err := readExecution(app.directory); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("execution retained: %v", err)
				}
				release, err := lockInstance(app.directory)
				if err != nil {
					t.Fatal(err)
				}
				if err := release(); err != nil {
					t.Fatal(err)
				}
				if !blockClose && sink.String() != "first" {
					t.Fatalf("queued writes survived abandonment: %q", sink.String())
				}
			})
		})
	}
}

type logLifecycleService struct {
	writer io.Writer
	fail   bool
}

func (s *logLifecycleService) Startup(context.Context) error {
	_, err := s.writer.Write([]byte("{\"phase\":\"startup\"}\n"))
	if s.fail {
		return errors.Join(err, errors.New("startup failure"))
	}
	return err
}
func (s *logLifecycleService) Shutdown(context.Context) error {
	_, err := s.writer.Write([]byte("{\"phase\":\"shutdown\"}\n"))
	return err
}

func TestAppLogLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "startup-failure"}[fail], func(t *testing.T) {
			app := testApp(t)
			if err := app.Register(&logLifecycleService{writer: app.LogWriter(), fail: fail}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(app.directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("LogWriter created files")
			}
			if fail {
				if err := app.Run(t.Context()); err == nil {
					t.Fatal("expected startup failure")
				}
			} else {
				cancel, done := runTestApp(t, app)
				_, _ = connectTestApp(t, app)
				_, _ = diagnosticLogWriter{&app.logs}.Write([]byte("http diagnostic\n"))
				cancel()
				if err := awaitApp(t, done); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(filepath.Join(app.directory, "logs", "daemon.log"))
			if err != nil {
				t.Fatal(err)
			}
			want := "{\"phase\":\"startup\"}\n"
			if !fail {
				want += "{\"phase\":\"shutdown\"}\n"
			}
			if string(data) != want {
				t.Fatalf("lifecycle log: %q", data)
			}
			if !fail {
				data, err = os.ReadFile(filepath.Join(app.directory, "logs", "daemon.stderr"))
				if err != nil || string(data) != "http diagnostic\n" {
					t.Fatalf("diagnostic log: %q %v", data, err)
				}
			}
			if _, err := app.LogWriter().Write([]byte("after")); !errors.Is(err, os.ErrClosed) {
				t.Fatal("writer remained active")
			}
		})
	}
}

func TestManagerCapturesSeparateLogs(t *testing.T) {
	manager, _ := testManager(t)
	t.Setenv("DAEMONKIT_TEST_LOGS", "1")
	if err := manager.EnsureRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		status.LogPath:    "{\"phase\":\"shutdown\"}",
		status.StdoutPath: "raw stdout",
		status.StderrPath: "raw stderr",
	} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s: %q %v", path, data, err)
		}
	}
	data, err := os.ReadFile(status.LogPath)
	if err != nil || strings.Contains(string(data), "raw") {
		t.Fatal("raw output polluted managed log")
	}
}

func TestLogOpenFailureReleasesExecution(t *testing.T) {
	app := testApp(t)
	dir, err := prepareLogDirectory(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "daemon.log"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(t.Context()); err == nil {
		t.Fatal("invalid log file accepted")
	}
	if _, err := app.LogWriter().Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := readExecution(app.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("execution retained")
	}
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestLogCleanupDoesNotOverwriteChangedOwnership(t *testing.T) {
	app := testApp(t)
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: "original", LaunchNonce: "original"}
	changed := record
	changed.LaunchNonce = "replacement"
	if err := writeExecution(app.directory, changed); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		app.running = true
		s := app.logs.start(&testLogSink{}, nil, nil)
		if err := app.finishRun(unlock, nil, &record, s); err == nil {
			t.Fatal("changed ownership ignored")
		}
		current, err := readExecution(app.directory)
		if err != nil || current != changed {
			t.Fatalf("changed ownership overwritten: %+v %v", current, err)
		}
	})
}
