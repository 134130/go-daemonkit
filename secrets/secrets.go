// Package secrets stores opaque values in the current user's OS credential store.
//
// Protection is provided by the OS at rest, subject to its access policy. This
// package does not require biometrics, provide hardware signing, or protect
// plaintext against process memory inspection. Returned bytes and backend copies
// may remain in memory; clearing a slice does not erase every copy.
//
// No plaintext file fallback, value logging, transaction, or enumeration is
// provided. Namespaces prevent naming collisions, not access by other processes
// running as the same user. OS access may prompt the user and may block.
package secrets

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrNotFound = errors.New("secret not found")
	// ErrAccess means the OS operation failed. It does not imply the item is absent.
	// Native error text is suppressed because it may contain sensitive payloads.
	ErrAccess              = errors.New("secret store access failed")
	ErrUnavailable         = errors.New("secret store unavailable")
	ErrPermission          = errors.New("secret store access denied")
	ErrInteractionRequired = errors.New("secret store requires user interaction")
	ErrCanceled            = errors.New("secret store operation canceled by user")
	ErrLimit               = errors.New("secret size limit exceeded")
	ErrInvalid             = errors.New("invalid secret identifier")
)

type backend interface {
	get(service, key string) ([]byte, error)
	set(service, key string, value []byte) error
	delete(service, key string) error
}

// Store is a namespace in the OS credential store. It has no Close or cache.
// Context cancellation is checked before the OS call; in-flight native calls
// cannot be canceled. A completed write is reported as successful even if the
// context was canceled while the OS call ran. Failed writes must not be assumed
// to have left the previous value unchanged.
type Store struct {
	service string
	backend backend
}

// New creates a handle without accessing the OS store. Scope components must
// contain 1–64 ASCII letters, digits, dots, underscores or hyphens, excluding
// "." and "..". App.Secrets supplies the application's normalized identity.
func New(appID, instanceID, namespace string) (*Store, error) {
	for _, part := range []string{appID, instanceID, namespace} {
		if part == "" || len(part) > 64 || part == "." || part == ".." {
			return nil, ErrInvalid
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				return nil, ErrInvalid
			}
		}
	}
	return &Store{service: "daemonkit/" + appID + "/" + instanceID + "/" + namespace, backend: nativeBackend{}}, nil
}

func (s *Store) validate(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.backend == nil {
		return ErrUnavailable
	}
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		return ErrInvalid
	}
	return nil
}

// Get returns an owned byte slice, or ErrNotFound. Keys are nonempty UTF-8,
// contain no NUL, and have at most 1 KiB. Keys are metadata, not secret values.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.validate(ctx, key); err != nil {
		return nil, err
	}
	return s.backend.get(s.service, key)
}

// Set creates or replaces a value. Empty values are allowed. Values are limited
// to 2,560 bytes for portability to Windows Credential Manager.
func (s *Store) Set(ctx context.Context, key string, value []byte) error {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	if len(value) > 2560 {
		return ErrLimit
	}
	return s.backend.set(s.service, key, value)
}

// Delete removes an item; deleting a missing item succeeds.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.validate(ctx, key); err != nil {
		return err
	}
	err := s.backend.delete(s.service, key)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
