package config

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"testing/synctest"
)

func TestServiceSerializesLoadThroughCommit(t *testing.T) {
	f := testFile(t, "[daemon]\ntimeout='initial'\n")
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var applied []string
		s, err := NewService(f, func(ctx context.Context, value settings) (Prepared, error) {
			if value.Daemon.Timeout == "first" {
				close(entered)
				<-release
			}
			return Prepared{Commit: func(context.Context) error { applied = append(applied, value.Daemon.Timeout); return nil }}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Reload(t.Context()); !errors.Is(err, ErrNotRunning) {
			t.Fatal(err)
		}
		if err := s.Startup(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := f.Set(t.Context(), "daemon.timeout", `'first'`); err != nil {
			t.Fatal(err)
		}
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { first <- s.Reload(t.Context()) }()
		<-entered
		if err := f.Set(t.Context(), "daemon.timeout", `'second'`); err != nil {
			t.Fatal(err)
		}
		go func() { second <- s.Reload(t.Context()) }()
		synctest.Wait()
		if s.Status().Revision != 1 {
			t.Fatal("published before commit")
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := <-second; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(applied, []string{"initial", "first", "second"}) {
			t.Fatal(applied)
		}
		if err := s.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := s.Reload(t.Context()); err == nil {
			t.Fatal("reload after shutdown")
		}
	})
}

func TestServiceCancellationFailureAndCleanup(t *testing.T) {
	for _, mode := range []string{"prepare", "commit", "shutdown", "after-commit"} {
		t.Run(mode, func(t *testing.T) {
			f := testFile(t, "[daemon]\ntimeout='initial'\n")
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				discarded := 0
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				s, err := NewService(f, func(ctx context.Context, v settings) (Prepared, error) {
					p := Prepared{Commit: func(context.Context) error { return nil }, Discard: func() { discarded++ }}
					if v.Daemon.Timeout == "initial" {
						return p, nil
					}
					switch mode {
					case "prepare":
						return p, errors.New("prepare failed")
					case "commit":
						p.Commit = func(context.Context) error { return errors.New("commit failed") }
					case "shutdown":
						close(entered)
						<-ctx.Done()
						return p, ctx.Err()
					case "after-commit":
						p.Commit = func(context.Context) error { cancel(); return nil }
					}
					return p, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Startup(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := f.Set(t.Context(), "daemon.timeout", `'next'`); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() { result <- s.Reload(ctx) }()
				if mode == "shutdown" {
					<-entered
					if err := s.Shutdown(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				err = <-result
				if mode == "after-commit" {
					if err != nil || s.Status().Revision != 2 || discarded != 0 {
						t.Fatalf("committed: %v %+v %d", err, s.Status(), discarded)
					}
				} else if err == nil || s.Status().Revision != 1 || discarded != 1 || s.Status().LastError == nil {
					t.Fatalf("failed: %v %+v %d", err, s.Status(), discarded)
				}
				if err := s.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestInvalidReloadKeepsRevision(t *testing.T) {
	f := testFile(t, "[daemon]\ntimeout='initial'\n")
	count := 0
	s, err := NewService(f, func(context.Context, settings) (Prepared, error) {
		return Prepared{Commit: func(context.Context) error { count++; return nil }}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Startup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Path(), []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(t.Context()); err == nil {
		t.Fatal("invalid reload succeeded")
	}
	if s.Status().Revision != 1 || count != 1 {
		t.Fatal("invalid candidate applied")
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
