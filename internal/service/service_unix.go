//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func managePlatform(ctx context.Context, action string, config Config) (string, error) {
	const unit = "crewfold.service"
	if action == "install" {
		for _, directory := range []string{config.DataDir, filepath.Dir(config.Endpoint), filepath.Dir(config.DefinitionPath)} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return "", err
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				return "", err
			}
		}
		if err := writeAtomic(config.DefinitionPath, []byte(systemdUnit(config))); err != nil {
			return "", err
		}
		for _, command := range [][]string{{"daemon-reload"}, {"enable", unit}, {"restart", unit}} {
			if output, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, command...)...).CombinedOutput(); err != nil {
				return "", fmt.Errorf("systemctl %s: %s: %w", strings.Join(command, " "), strings.TrimSpace(string(output)), err)
			}
		}
	} else if action == "start" || action == "stop" {
		if output, err := exec.CommandContext(ctx, "systemctl", "--user", action, unit).CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl %s: %s: %w", action, strings.TrimSpace(string(output)), err)
		}
	}
	output, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", unit).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect service: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func systemdUnit(config Config) string {
	return `[Unit]
Description=Crewfold shared AI rooms
After=default.target

[Service]
Type=simple
ExecStart=` + systemdQuote(config.Executable) + ` daemon run --data-dir ` + systemdQuote(config.DataDir) + ` --socket ` + systemdQuote(config.Endpoint) + ` --web-address 127.0.0.1:0
Restart=on-failure
RestartSec=2s
UMask=0077
NoNewPrivileges=true
RuntimeDirectory=crewfold
RuntimeDirectoryMode=0700

[Install]
WantedBy=default.target
`
}

func systemdQuote(value string) string { return strings.ReplaceAll(strconv.Quote(value), "%", "%%") }

func writeAtomic(path string, content []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
