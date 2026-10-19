package secrets

import (
	"errors"
	"os"

	"github.com/godbus/dbus/v5"
)

func platformError(err error) error {
	var bus *dbus.Error
	var value dbus.Error
	name := ""
	if errors.As(err, &bus) {
		name = bus.Name
	} else if errors.As(err, &value) {
		name = value.Name
	}
	switch name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner", "org.freedesktop.DBus.Error.NoServer":
		return ErrUnavailable
	case "org.freedesktop.DBus.Error.AccessDenied":
		return ErrPermission
	case "org.freedesktop.Secret.Error.IsLocked":
		return ErrInteractionRequired
	}
	if errors.Is(err, os.ErrPermission) {
		return ErrPermission
	}
	return ErrAccess
}
