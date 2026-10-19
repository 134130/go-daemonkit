//go:build darwin && cgo

package secrets

import (
	"errors"
	"github.com/keybase/go-keychain"
	"testing"
)

func TestNativeErrorRedaction(t *testing.T) {
	for _, tc := range []struct{ in, want error }{
		{nil, nil}, {keychain.ErrorItemNotFound, ErrNotFound},
		{keychain.ErrorAuthFailed, ErrPermission}, {keychain.ErrorInteractionNotAllowed, ErrInteractionRequired},
		{keychain.ErrorNotAvailable, ErrUnavailable}, {keychain.ErrorUserCanceled, ErrCanceled},
		{errors.New("secret payload"), ErrAccess},
	} {
		if got := nativeError(tc.in); got != tc.want {
			t.Fatalf("error = %v, want %v", got, tc.want)
		}
	}
}
