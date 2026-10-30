package config

import (
	"golang.org/x/sys/windows"
	"os"
)

func openRead(path string) (*os.File, error) { return os.Open(path) }

func privateFile(file *os.File) error    { return privatePath(file.Name(), "") }
func privateDirectory(path string) error { return privatePath(path, "OICI") }

func privatePath(path, inheritance string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;" + inheritance + ";GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
