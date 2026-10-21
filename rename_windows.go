package daemon

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func renameFile(oldpath, newpath string) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Rename(oldpath, newpath)
		// Scanners and readers can briefly hold handles without FILE_SHARE_DELETE.
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
