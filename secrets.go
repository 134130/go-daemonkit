package daemon

import "github.com/134130/go-daemonkit/secrets"

// Secrets returns a namespace handle without accessing the OS credential store.
// Inject it into services explicitly. Unlike SQLite, the OS owns its lifetime;
// it is usable outside Run and has no Close. Required credentials should be read
// in Startup so access failures prevent readiness. macOS support requires cgo.
func (a *App) Secrets(namespace string) (*secrets.Store, error) {
	return secrets.New(a.options.AppID, a.options.InstanceID, namespace)
}
