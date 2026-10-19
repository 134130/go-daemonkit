package secrets

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestNativeErrorRedaction(t *testing.T) {
	for _, tc := range []struct{ in, want error }{
		{&dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"secret payload"}}, ErrUnavailable},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}, ErrPermission},
		{&dbus.Error{Name: "org.freedesktop.Secret.Error.IsLocked"}, ErrInteractionRequired},
		{errors.New("secret payload"), ErrAccess},
	} {
		if got := nativeError(tc.in); got != tc.want {
			t.Fatalf("error = %v, want %v", got, tc.want)
		}
	}
}
