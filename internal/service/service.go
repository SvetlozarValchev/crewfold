// Package service manages Crewfold as an owner-local background process.
package service

import (
	"context"
	"errors"
)

// Config describes one installed daemon instance.
type Config struct {
	Executable     string
	DataDir        string
	Endpoint       string
	DefinitionPath string
}

// Result is the platform-neutral service status returned by the CLI.
type Result struct {
	Action         string `json:"action"`
	Status         string `json:"status"`
	DataDir        string `json:"data_dir"`
	Endpoint       string `json:"socket"`
	DefinitionPath string `json:"unit"`
}

// Manage validates action and delegates to the host service manager.
func Manage(ctx context.Context, action string, config Config) (Result, error) {
	switch action {
	case "install", "start", "stop", "status":
	default:
		return Result{}, errors.New("service action must be install, start, stop, or status")
	}
	status, err := managePlatform(ctx, action, config)
	if err != nil {
		return Result{}, err
	}
	return Result{Action: action, Status: status, DataDir: config.DataDir, Endpoint: config.Endpoint, DefinitionPath: config.DefinitionPath}, nil
}
