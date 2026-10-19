package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
)

type memoryBackend struct {
	values map[string][]byte
	calls  int
	err    error
}

func (b *memoryBackend) get(service, key string) ([]byte, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	value, ok := b.values[service+":"+key]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(value), nil
}
func (b *memoryBackend) set(service, key string, value []byte) error {
	b.calls++
	if b.err != nil {
		return b.err
	}
	b.values[service+":"+key] = bytes.Clone(value)
	return nil
}
func (b *memoryBackend) delete(service, key string) error {
	b.calls++
	if b.err != nil {
		return b.err
	}
	if _, ok := b.values[service+":"+key]; !ok {
		return ErrNotFound
	}
	delete(b.values, service+":"+key)
	return nil
}

func TestIsolationAndValidation(t *testing.T) {
	b := &memoryBackend{values: map[string][]byte{}}
	s, err := New("app", "default", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	s.backend = b
	value := []byte{0, 255, 10}
	if err := s.Set(t.Context(), "token", value); err != nil {
		t.Fatal(err)
	}
	value[0] = 42
	got, err := s.Get(t.Context(), "token")
	if err != nil || !bytes.Equal(got, []byte{0, 255, 10}) {
		t.Fatalf("Get = %v, %v", got, err)
	}
	for _, scope := range [][3]string{{"other", "default", "oauth"}, {"app", "other", "oauth"}, {"app", "default", "other"}} {
		other, err := New(scope[0], scope[1], scope[2])
		if err != nil {
			t.Fatal(err)
		}
		other.backend = b
		if _, err := other.Get(t.Context(), "token"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("scope leaked: %v", err)
		}
	}
	before := b.calls
	for _, key := range []string{"", "bad\x00key", string([]byte{255}), string(bytes.Repeat([]byte("x"), 1025))} {
		if err := s.Set(t.Context(), key, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid = %v", err)
		}
	}
	if err := s.Set(t.Context(), "token", make([]byte, 2561)); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Set(ctx, "token", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.calls != before {
		t.Fatal("rejected request reached backend")
	}
	for range 2 {
		if err := s.Delete(t.Context(), "token"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set(t.Context(), "empty", nil); err != nil {
		t.Fatal(err)
	}
	if value, err := s.Get(t.Context(), "empty"); err != nil || len(value) != 0 {
		t.Fatalf("empty = %v, %v", value, err)
	}
	b.err = ErrAccess
	if err := s.Set(t.Context(), "failure", nil); !errors.Is(err, ErrAccess) {
		t.Fatal(err)
	}
	if _, ok := b.values[s.service+":failure"]; ok {
		t.Fatal("failed write stored value")
	}
	for _, scope := range [][3]string{{"", "default", "ns"}, {"app", "../", "ns"}, {"app", "default", "a/b"}, {"app", "default", string(bytes.Repeat([]byte("a"), 65))}} {
		if _, err := New(scope[0], scope[1], scope[2]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid scope accepted: %v", err)
		}
	}
}

// Opt in only with a disposable, unlocked OS credential store.
func TestNativeRoundTrip(t *testing.T) {
	if os.Getenv("DAEMONKIT_TEST_KEYCHAIN") != "1" {
		t.Skip("requires an OS credential store")
	}
	store, err := New("daemonkit-test", fmt.Sprintf("p%d", os.Getpid()), "native")
	if err != nil {
		t.Fatal(err)
	}
	key := "roundtrip"
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), key); err != nil {
			t.Error(err)
		}
	})
	for _, value := range [][]byte{[]byte("first"), {0, 255, 10}, nil, bytes.Repeat([]byte("x"), 2560)} {
		if err := store.Set(t.Context(), key, value); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(t.Context(), key)
		if err != nil || !bytes.Equal(got, value) {
			t.Fatalf("roundtrip mismatch: %v", err)
		}
	}
	for range 2 {
		if err := store.Delete(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}
