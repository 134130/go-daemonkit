package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestInstanceDirectoryExplicitOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app", "instance")
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := prepareInstanceDirectory(path); err != nil {
			t.Fatal(err)
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil {
			t.Fatal(err)
		}
		if owner == nil || !owner.Equals(user.User.Sid) {
			t.Fatal("instance directory is not owned by the current user")
		}
	}
}

func TestInstanceDirectoryRejectsDifferentDefaultOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner != nil && owner.Equals(user.User.Sid) {
		t.Skip("default owner is already the current user")
	}
	if err := prepareInstanceDirectory(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("foreign owner = %v", err)
	}
}
