//go:build !linux && !windows && !darwin

package service

import (
	"context"
	"errors"
)

func managePlatform(context.Context, string, Config) (string, error) {
	return "", errors.New("background service integration is not implemented on this platform; run `crewfold daemon run` in the foreground")
}
