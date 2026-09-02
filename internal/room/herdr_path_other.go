//go:build !windows

package room

import (
	"os"
	"path/filepath"
)

func (r *HerdrStewardRuntime) herdrEnvironmentPath(_ string) (string, error) {
	pathValue := filepath.Dir(r.crewfoldPath)
	if inherited := os.Getenv("PATH"); inherited != "" {
		pathValue += string(os.PathListSeparator) + inherited
	}
	return pathValue, nil
}
