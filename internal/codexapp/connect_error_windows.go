//go:build windows

package codexapp

import "fmt"

func codexConnectError(path string, err error) error {
	return fmt.Errorf("%w; control socket %q is unavailable: %v", ErrDaemonLifecycleUnsupported, path, err)
}
