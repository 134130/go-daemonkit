package daemon

import (
	"fmt"
	"os"
)

func openLogFile(path string, flags int) (*os.File, error) {
	if err := checkLogPath(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err == nil {
		current, readErr := os.Lstat(path)
		err = readErr
		if err == nil && (!current.Mode().IsRegular() || !opened.Mode().IsRegular() || !os.SameFile(opened, current)) {
			err = fmt.Errorf("log file changed during open")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
