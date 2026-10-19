package daemon

import (
	"context"
	"fmt"

	"github.com/134130/go-daemonkit/storage"
)

// Storage validates a namespace without opening files. The handle is usable in
// Service Startup, RPC methods, and Shutdown, while App.Run owns the database.
func (a *App) Storage(namespace string) (*storage.Store, error) {
	if !validName(namespace) {
		return nil, fmt.Errorf("invalid storage namespace")
	}
	return a.storage.Namespace(namespace), nil
}

func (a *App) openStorage(ctx context.Context) error {
	return a.storage.Open(ctx, a.directory)
}

func (a *App) closeStorage() error { return a.storage.Close() }
