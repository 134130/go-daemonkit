//go:build (darwin && !cgo) || (!darwin && !linux && !windows)

package secrets

type nativeBackend struct{}

func (nativeBackend) get(string, string) ([]byte, error) { return nil, ErrUnavailable }
func (nativeBackend) set(string, string, []byte) error   { return ErrUnavailable }
func (nativeBackend) delete(string, string) error        { return ErrUnavailable }
