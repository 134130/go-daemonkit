package daemon

import (
	"context"
	"errors"
	"github.com/134130/go-daemonkit/storage"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func testStorage(t *testing.T) (*App, *storage.Store) {
	t.Helper()
	a := testApp(t)
	if err := os.MkdirAll(a.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.openStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.closeStorage(); err != nil {
			t.Error(err)
		}
	})
	store, err := a.Storage("test")
	if err != nil {
		t.Fatal(err)
	}
	return a, store
}

func TestStoragePersistenceAndIsolation(t *testing.T) {
	a, store := testStorage(t)
	if err := store.Update(t.Context(), func(tx *storage.WriteTx) error {
		for _, key := range []string{"prefix_b", "prefix_a", "prefix%literal", "other"} {
			if err := tx.Set(key, []byte(key)); err != nil {
				return err
			}
		}
		return tx.Set("empty", nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.closeStorage(); err != nil {
		t.Fatal(err)
	}
	if err := store.View(t.Context(), func(*storage.ReadTx) error { return nil }); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("closed = %v", err)
	}
	if err := a.openStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error {
		keys, err := tx.List("prefix_")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(keys, []string{"prefix_a", "prefix_b"}) {
			t.Fatalf("prefix keys = %v", keys)
		}
		value, err := tx.Get("prefix_a")
		if err != nil || string(value) != "prefix_a" {
			t.Fatalf("persisted = %q, %v", value, err)
		}
		value, err = tx.Get("empty")
		if err != nil || len(value) != 0 {
			t.Fatalf("empty = %q, %v", value, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	other, _ := a.Storage("other")
	if err := other.View(t.Context(), func(tx *storage.ReadTx) error { _, err := tx.Get("prefix_a"); return err }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("namespace isolation = %v", err)
	}
	isolated, err := New(Options{AppID: a.options.AppID, InstanceID: "another", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(isolated.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := isolated.openStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer isolated.closeStorage()
	isolatedStore, _ := isolated.Storage("test")
	if err := isolatedStore.View(t.Context(), func(tx *storage.ReadTx) error { _, err := tx.Get("prefix_a"); return err }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("instance isolation = %v", err)
	}
}

func TestStorageRollbackAndExpiredTransaction(t *testing.T) {
	_, store := testStorage(t)
	failure := errors.New("callback failed")
	var escaped *storage.WriteTx
	err := store.Update(t.Context(), func(tx *storage.WriteTx) error {
		escaped = tx
		if err := tx.Set("key", []byte("value")); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := escaped.Set("late", nil); !errors.Is(err, storage.ErrTransactionClosed) {
		t.Fatalf("escaped = %v", err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error { _, err := tx.Get("key"); return err }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() != "panic" {
				t.Error("panic not propagated")
			}
		}()
		_ = store.Update(t.Context(), func(tx *storage.WriteTx) error {
			if err := tx.Set("panic", nil); err != nil {
				t.Fatal(err)
			}
			panic("panic")
		})
	}()
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error { _, err := tx.Get("panic"); return err }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err = store.Update(ctx, func(tx *storage.WriteTx) error {
		if err := tx.Set("canceled", nil); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error { _, err := tx.Get("canceled"); return err }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStorageSnapshotAndConcurrentUpdates(t *testing.T) {
	_, store := testStorage(t)
	if err := store.Update(t.Context(), func(tx *storage.WriteTx) error { return tx.Set("count", []byte("0")) }); err != nil {
		t.Fatal(err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error {
		before, err := tx.Get("count")
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() {
			done <- store.Update(t.Context(), func(tx *storage.WriteTx) error { return tx.Set("count", []byte("1")) })
		}()
		if err := <-done; err != nil {
			return err
		}
		after, err := tx.Get("count")
		if string(before) != string(after) {
			t.Fatalf("snapshot changed: %s -> %s", before, after)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			err := store.Update(t.Context(), func(tx *storage.WriteTx) error {
				value, err := tx.Get("count")
				if err != nil {
					return err
				}
				number, err := strconv.Atoi(string(value))
				if err != nil {
					return err
				}
				return tx.Set("count", []byte(strconv.Itoa(number+1)))
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error {
		value, err := tx.Get("count")
		if string(value) != "21" {
			t.Errorf("lost update: %q", value)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageLimitsAndReadOnly(t *testing.T) {
	_, store := testStorage(t)
	err := store.Update(t.Context(), func(tx *storage.WriteTx) error {
		if err := tx.Set("valid", nil); err != nil {
			return err
		}
		_ = tx.Set("large", make([]byte, (1<<20)+1))
		return nil
	})
	if !errors.Is(err, storage.ErrStorageLimit) {
		t.Fatalf("ignored write failure = %v", err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error {
		forged := &storage.WriteTx{ReadTx: *tx}
		if err := forged.Set("forbidden", nil); err == nil {
			t.Fatal("read-only write accepted")
		}
		_, err := tx.Get("valid")
		return err
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if err := store.Update(t.Context(), func(tx *storage.WriteTx) error {
		for i := range 1001 {
			if err := tx.Set(strconv.Itoa(i), nil); err != nil {
				return err
			}
		}
		return tx.Delete("absent")
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error {
		keys, err := tx.List("")
		if keys != nil {
			t.Fatal("partial list returned")
		}
		return err
	}); !errors.Is(err, storage.ErrStorageLimit) {
		t.Fatal(err)
	}
}

func TestStorageLifecycle(t *testing.T) {
	a := testApp(t)
	store, err := a.Storage("service")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Storage created files")
	}
	if err := store.View(t.Context(), func(*storage.ReadTx) error { return nil }); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatal(err)
	}
	if err := a.Register(&firstService{testHooks{
		start: func(ctx context.Context) error {
			return store.Update(ctx, func(tx *storage.WriteTx) error { return tx.Set("started", nil) })
		},
		stop: func(ctx context.Context) error {
			return store.Update(ctx, func(tx *storage.WriteTx) error { return tx.Set("stopped", nil) })
		},
	}}); err != nil {
		t.Fatal(err)
	}
	cancel, done := runTestApp(t, a)
	connectTestApp(t, a)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := store.View(t.Context(), func(*storage.ReadTx) error { return nil }); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatal(err)
	}
	if err := a.openStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer a.closeStorage()
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error { _, err := tx.Get("stopped"); return err }); err != nil {
		t.Fatal(err)
	}
}

func TestStorageInitializationFailurePreservesData(t *testing.T) {
	a := testApp(t)
	if err := os.MkdirAll(a.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.directory, "storage.sqlite")
	data := []byte("not a database")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(t.Context()); err == nil {
		t.Fatal("corrupt storage became Ready")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatalf("corrupt file modified: %q, %v", after, err)
	}
	unlock, err := lockInstance(a.directory)
	if err != nil {
		t.Fatal(err)
	}
	_ = unlock()
}

func TestStorageWriterWaitCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, store := testStorage(t)
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- store.Update(t.Context(), func(*storage.WriteTx) error { close(entered); <-release; return nil })
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		called := false
		err := store.Update(ctx, func(*storage.WriteTx) error { called = true; return nil })
		close(release)
		if firstErr := <-done; firstErr != nil {
			t.Fatal(firstErr)
		}
		if !errors.Is(err, context.DeadlineExceeded) || called {
			t.Fatalf("writer wait = %v, callback = %v", err, called)
		}
	})
}

func TestStorageCrashHelper(t *testing.T) {
	directory := os.Getenv("DAEMONKIT_STORAGE_CRASH_DIRECTORY")
	if directory == "" {
		return
	}
	app := &App{directory: directory}
	if err := app.openStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	store, _ := app.Storage("test")
	_ = store.Update(t.Context(), func(tx *storage.WriteTx) error {
		if err := tx.Set("committed", []byte("unfinished")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Set("partial", []byte("unfinished")); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
		return nil
	})
	t.Fatal("crash helper returned")
}

func TestStorageCrashPreservesCommittedData(t *testing.T) {
	a, store := testStorage(t)
	if err := store.Update(t.Context(), func(tx *storage.WriteTx) error { return tx.Set("committed", []byte("original")) }); err != nil {
		t.Fatal(err)
	}
	if err := a.closeStorage(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestStorageCrashHelper$")
	child.Env = append(os.Environ(), "DAEMONKIT_STORAGE_CRASH_DIRECTORY="+a.directory)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v: %s", err, output)
	}
	if err := a.openStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.View(t.Context(), func(tx *storage.ReadTx) error {
		value, err := tx.Get("committed")
		if err != nil || string(value) != "original" {
			t.Fatalf("committed value = %q, %v", value, err)
		}
		_, err = tx.Get("partial")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("partial change visible: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
