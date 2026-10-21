//go:build darwin || linux

package daemon

import "os"

func renameFile(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
