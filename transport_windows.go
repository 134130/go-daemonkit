package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func localPipeName(path string) bool {
	const prefix = `\\.\pipe\`
	return strings.HasPrefix(path, prefix) && len(path) > len(prefix) && !strings.ContainsAny(path[len(prefix):], `\/:`)
}

func listenLocal(path string) (net.Listener, error) {
	if !localPipeName(path) {
		return nil, fmt.Errorf("expected a local named pipe path")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid.String()
	// go-winio creates the first instance exclusively and rejects remote clients.
	return winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "O:" + sid + "D:P(A;;GA;;;" + sid + ")",
	})
}

func dialLocal(ctx context.Context, path string) (net.Conn, error) {
	if !localPipeName(path) {
		return nil, fmt.Errorf("expected a local named pipe path")
	}
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := verifyPipeOwner(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func verifyPipeOwner(conn net.Conn) error {
	handle, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("named pipe does not expose its OS handle")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(handle.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return fmt.Errorf("named pipe is not owned by the current user: %w", os.ErrPermission)
	}
	return nil
}
