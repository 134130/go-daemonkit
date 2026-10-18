package daemon

import "fmt"

func killOwnedProcess(record executionRecord) error {
	// shortcut: macOS has no pidfd; retain fail-closed behavior until safe termination is available.
	return fmt.Errorf("shutdown grace expired for PID %d: safe force termination is unavailable on macOS", record.PID)
}
