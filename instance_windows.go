package daemon

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func instanceKey(directory string) (string, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid := user.User.Sid.String()
	key := sha256.Sum256([]byte(sid + "\x00" + strings.ToLower(directory)))
	return fmt.Sprintf("daemonkit-%x", key), sid, nil
}

func instanceEndpoint(directory string) (string, error) {
	key, _, err := instanceKey(directory)
	return `\\.\pipe\` + key, err
}

func lockInstance(directory string) (func() error, error) {
	key, sid, err := instanceKey(directory)
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;GA;;;" + sid + ")")
	if err != nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(`Global\` + key)
	if err != nil {
		return nil, err
	}
	// Existence is the lease; no thread-affine mutex acquisition is used.
	handle, err := windows.CreateMutex(attributes, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	return func() error { return windows.CloseHandle(handle) }, nil
}
