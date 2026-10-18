package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestAppRecoversAfterProcessCrash(t *testing.T) {
	app := testApp(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestAppCrashHelper$")
	command.Env = append(os.Environ(), "DAEMONKIT_CRASH_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	_, previous := connectTestApp(t, app)
	record, err := readExecution(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	if record.PID != command.Process.Pid || record.LaunchNonce != previous.LaunchNonce {
		t.Fatalf("incorrect process record: %+v", record)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	waited = true
	cancel, done := runTestApp(t, app)
	_, current := connectTestApp(t, app)
	if current.LaunchNonce == previous.LaunchNonce {
		t.Fatal("recovery reused execution nonce")
	}
	cancel()
	if err := awaitApp(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := readExecution(app.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record retained after clean shutdown: %v", err)
	}
}

func TestAppCrashHelper(t *testing.T) {
	if os.Getenv("DAEMONKIT_CRASH_HELPER") != "1" {
		return
	}
	app, err := New(Options{AppID: "test", Version: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionRecordOwnership(t *testing.T) {
	app := testApp(t)
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: identity, LaunchNonce: "first"}
	if err := writeExecution(app.directory, record); err != nil {
		t.Fatal(err)
	}
	if err := recoverExecution(app.directory, app.endpoint); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("live process was not protected: %v", err)
	}
	replacement := record
	replacement.LaunchNonce = "replacement"
	if err := writeExecution(app.directory, replacement); err != nil {
		t.Fatal(err)
	}
	if err := removeExecution(app.directory, record); err == nil {
		t.Fatal("removed another generation's record")
	}
	actual, err := readExecution(app.directory)
	if err != nil || actual != replacement {
		t.Fatalf("replacement record modified: %+v, %v", actual, err)
	}
}

func TestMalformedExecutionRecordBlocksRecovery(t *testing.T) {
	app := testApp(t)
	unlock, err := lockInstance(app.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := os.WriteFile(filepath.Join(app.directory, "runtime.json"), []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverExecution(app.directory, app.endpoint); err == nil {
		t.Fatal("incomplete record accepted")
	}
}

func TestOperationLockCancellation(t *testing.T) {
	app := testApp(t)
	synctest.Test(t, func(t *testing.T) {
		unlock, err := lockOperation(t.Context(), app.directory)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		// A child can hold the runtime lock while its parent holds the operation lock.
		unlockRuntime, err := lockInstance(app.directory)
		if err != nil {
			t.Fatal(err)
		}
		defer unlockRuntime()
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		defer cancel()
		start := time.Now()
		if unlock, err := lockOperation(ctx, app.directory); !errors.Is(err, context.DeadlineExceeded) {
			if unlock != nil {
				_ = unlock()
			}
			t.Fatalf("contending lock = %v", err)
		}
		if elapsed := time.Since(start); elapsed != 25*time.Millisecond {
			t.Fatalf("deadline elapsed = %v", elapsed)
		}
	})
}
