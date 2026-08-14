//go:build !linux

package daemon

import "errors"

func daemonProcessResources() (int64, int64, error) {
	return 0, 0, errors.New("process resource probes require Linux")
}
