//go:build darwin || linux

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStaleEndpointRequiresRecordedIdentity(t *testing.T) {
	for _, matching := range []bool{true, false} {
		name := "mismatched"
		if matching {
			name = "matching"
		}
		t.Run(name, func(t *testing.T) {
			path := testLocalPath(t)
			unlock, err := lockInstance(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			listener, err := listenLocal(path)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := socketIdentity(path)
			if err != nil {
				t.Fatal(err)
			}
			// Close the FD but deliberately retain the socket, as a crash would.
			if err := listener.(*localListener).UnixListener.Close(); err != nil {
				t.Fatal(err)
			}
			if !matching {
				identity += "-different"
			}
			record := executionRecord{PID: os.Getpid(), ProcessIdentity: "previous-process-at-same-pid", LaunchNonce: "old", SocketIdentity: identity}
			if err := writeExecution(filepath.Dir(path), record); err != nil {
				t.Fatal(err)
			}
			err = recoverExecution(filepath.Dir(path), path)
			if matching {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stale socket retained: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("unowned socket removed")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := processIdentity(os.Getpid()); err != nil {
				t.Fatalf("current process affected: %v", err)
			}
		})
	}
}

func TestEndpointIsPublishedAfterRecord(t *testing.T) {
	path := testLocalPath(t)
	listener, identity, publish, err := prepareExecutionEndpoint(path, "12345678-more-nonce")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("endpoint published early: %v", err)
	}
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: "test", LaunchNonce: "12345678-more-nonce", SocketIdentity: identity}
	if err := writeExecution(filepath.Dir(path), record); err != nil {
		t.Fatal(err)
	}
	if err := publish(); err != nil {
		t.Fatal(err)
	}
	got, err := socketIdentity(path)
	if err != nil || got != identity {
		t.Fatalf("published identity = %q, %v", got, err)
	}
}

func TestRecoveryPreservesAcceptingSocket(t *testing.T) {
	path := testLocalPath(t)
	listener, err := listenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	identity, err := socketIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeStaleEndpoint(path, executionRecord{SocketIdentity: identity}); err == nil {
		t.Fatal("accepting endpoint removed")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryBeforeEndpointPublication(t *testing.T) {
	path := testLocalPath(t)
	const nonce = "12345678-unpublished"
	listener, identity, _, err := prepareExecutionEndpoint(path, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.(*localListener).UnixListener.Close(); err != nil {
		t.Fatal(err)
	}
	record := executionRecord{PID: os.Getpid(), ProcessIdentity: "old-process", LaunchNonce: nonce, SocketIdentity: identity}
	if err := writeExecution(filepath.Dir(path), record); err != nil {
		t.Fatal(err)
	}
	if err := recoverExecution(filepath.Dir(path), path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(path), nonce[:8])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recorded staging socket retained: %v", err)
	}
}
