package daemon

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// shortcut: Native Windows runtime validation is deferred while the test environment
// is unavailable (2026-10-28); resume on a usable Windows session and record passing
// detached lifecycle, conditional update/journal recovery, environment rollback,
// logging shutdown/rotation, and client disconnection tests. ARM64 cross-builds and
// earlier logging/status tests do not establish correctness of these latest paths.
func configureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
}
