package secrets

import (
	"errors"
	"golang.org/x/sys/windows"
)

func platformError(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return ErrPermission
	case errors.Is(err, windows.ERROR_NO_SUCH_LOGON_SESSION):
		return ErrUnavailable
	case errors.Is(err, windows.ERROR_CANCELLED):
		return ErrCanceled
	default:
		return ErrAccess
	}
}
