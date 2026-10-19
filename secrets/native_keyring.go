//go:build linux || windows

package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

type nativeBackend struct{}

func (nativeBackend) get(service, key string) ([]byte, error) {
	value, err := keyring.Get(service, key)
	if err != nil {
		return nil, nativeError(err)
	}
	return []byte(value), nil
}

func (nativeBackend) set(service, key string, value []byte) error {
	return nativeError(keyring.Set(service, key, string(value)))
}

func (nativeBackend) delete(service, key string) error {
	return nativeError(keyring.Delete(service, key))
}

func nativeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return ErrLimit
	default:
		return platformError(err)
	}
}
