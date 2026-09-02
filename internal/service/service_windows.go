//go:build windows

package service

import (
	"context"
	"errors"
)

func managePlatform(context.Context, string, Config) (string, error) {
	return "", errors.New("Windows background service integration is not implemented yet; run `crewfold daemon run` in the foreground")
}
