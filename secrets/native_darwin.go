//go:build darwin && cgo

package secrets

import (
	"errors"

	"github.com/keybase/go-keychain"
)

type nativeBackend struct{}

func item(service, key string) keychain.Item {
	i := keychain.NewItem()
	i.SetSecClass(keychain.SecClassGenericPassword)
	i.SetService(service)
	i.SetAccount(key)
	return i
}

func (nativeBackend) get(service, key string) ([]byte, error) {
	i := item(service, key)
	i.SetMatchLimit(keychain.MatchLimitOne)
	i.SetReturnData(true)
	results, err := keychain.QueryItem(i)
	if err != nil {
		return nil, nativeError(err)
	}
	if len(results) == 0 {
		return nil, ErrNotFound
	}
	return results[0].Data, nil
}

func (nativeBackend) set(service, key string, value []byte) error {
	if value == nil {
		value = []byte{}
	}
	i := item(service, key)
	i.SetData(value)
	err := keychain.AddItem(i)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		changes := keychain.NewItem()
		changes.SetData(value)
		err = keychain.UpdateItem(item(service, key), changes)
	}
	return nativeError(err)
}

func (nativeBackend) delete(service, key string) error {
	return nativeError(keychain.DeleteItem(item(service, key)))
}

func nativeError(err error) error {
	switch err {
	case nil:
		return nil
	case keychain.ErrorItemNotFound:
		return ErrNotFound
	case keychain.ErrorNotAvailable, keychain.ErrorNoSuchKeychain:
		return ErrUnavailable
	case keychain.ErrorAuthFailed, keychain.ErrorNoAccessForItem:
		return ErrPermission
	case keychain.ErrorInteractionNotAllowed:
		return ErrInteractionRequired
	case keychain.ErrorUserCanceled:
		return ErrCanceled
	default:
		return ErrAccess
	}
}
