//go:build darwin

package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const launchdLabel = "dev.crewfold.Crewfold"

func managePlatform(ctx context.Context, action string, config Config) (string, error) {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	serviceTarget := domain + "/" + launchdLabel
	switch action {
	case "install":
		for _, directory := range []string{config.DataDir, filepath.Dir(config.Endpoint), filepath.Dir(config.DefinitionPath)} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return "", err
			}
		}
		definition, err := launchdPlist(config)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(config.DefinitionPath, []byte(definition), 0o600); err != nil {
			return "", fmt.Errorf("write launchd agent: %w", err)
		}
		if loaded, _ := launchdStatus(ctx, serviceTarget); loaded != "not-loaded" {
			if _, err := runLaunchctl(ctx, "bootout", serviceTarget); err != nil {
				return "", err
			}
		}
		if _, err := runLaunchctl(ctx, "bootstrap", domain, config.DefinitionPath); err != nil {
			return "", err
		}
		if _, err := runLaunchctl(ctx, "enable", serviceTarget); err != nil {
			return "", err
		}
		if _, err := runLaunchctl(ctx, "kickstart", "-k", serviceTarget); err != nil {
			return "", err
		}
	case "uninstall":
		status, err := launchdStatus(ctx, serviceTarget)
		if err != nil {
			return "", err
		}
		if status != "not-loaded" {
			if _, err := runLaunchctl(ctx, "bootout", serviceTarget); err != nil {
				return "", err
			}
		}
		if err := os.Remove(config.DefinitionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("remove launchd agent: %w", err)
		}
		return "not-installed", nil
	case "start":
		if _, err := os.Stat(config.DefinitionPath); errors.Is(err, os.ErrNotExist) {
			return "", errors.New("Crewfold launch agent is not installed; run `crewfold service install`")
		} else if err != nil {
			return "", err
		}
		status, err := launchdStatus(ctx, serviceTarget)
		if err != nil {
			return "", err
		}
		if status == "not-loaded" {
			if _, err := runLaunchctl(ctx, "bootstrap", domain, config.DefinitionPath); err != nil {
				return "", err
			}
		}
		if _, err := runLaunchctl(ctx, "kickstart", "-k", serviceTarget); err != nil {
			return "", err
		}
	case "stop":
		status, err := launchdStatus(ctx, serviceTarget)
		if err != nil {
			return "", err
		}
		if status == "not-loaded" {
			if _, err := os.Stat(config.DefinitionPath); errors.Is(err, os.ErrNotExist) {
				return "not-installed", nil
			}
			return "inactive", nil
		}
		if _, err := runLaunchctl(ctx, "bootout", serviceTarget); err != nil {
			return "", err
		}
		return "inactive", nil
	case "status":
		status, err := launchdStatus(ctx, serviceTarget)
		if err != nil {
			return "", err
		}
		if status == "not-loaded" {
			if _, statErr := os.Stat(config.DefinitionPath); errors.Is(statErr, os.ErrNotExist) {
				return "not-installed", nil
			}
			return "inactive", nil
		}
		return status, nil
	default:
		panic("service action was not validated")
	}
	status, err := launchdStatus(ctx, serviceTarget)
	if err != nil {
		return "", err
	}
	if status == "not-loaded" {
		return "starting", nil
	}
	return status, nil
}

func launchdStatus(ctx context.Context, target string) (string, error) {
	command := exec.CommandContext(ctx, "launchctl", "print", target)
	output, err := command.CombinedOutput()
	if err != nil {
		if command.ProcessState != nil && command.ProcessState.ExitCode() != 0 {
			return "not-loaded", nil
		}
		return "", fmt.Errorf("inspect launch agent: %s: %w", boundedLaunchctlOutput(output), err)
	}
	if strings.Contains(string(output), "state = running") {
		return "active", nil
	}
	return "inactive", nil
}

func runLaunchctl(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "launchctl", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("launchctl %s: %s: %w", strings.Join(arguments, " "), boundedLaunchctlOutput(output), err)
	}
	return output, nil
}

func launchdPlist(config Config) (string, error) {
	arguments := append([]string{config.Executable}, daemonArgumentsDarwin(config)...)
	values := make([]string, len(arguments))
	for index, argument := range arguments {
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(argument)); err != nil {
			return "", err
		}
		values[index] = "    <string>" + escaped.String() + "</string>"
	}
	logPath := filepath.Join(config.DataDir, "crewfold.log")
	var escapedLog bytes.Buffer
	if err := xml.EscapeText(&escapedLog, []byte(logPath)); err != nil {
		return "", err
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
` + strings.Join(values, "\n") + `
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>` + escapedLog.String() + `</string>
  <key>StandardErrorPath</key><string>` + escapedLog.String() + `</string>
</dict>
</plist>
`, nil
}

func daemonArgumentsDarwin(config Config) []string {
	return []string{"daemon", "run", "--data-dir", config.DataDir, "--socket", config.Endpoint, "--web-address", "127.0.0.1:0"}
}

func boundedLaunchctlOutput(output []byte) string {
	value := strings.TrimSpace(strings.ToValidUTF8(string(output), "�"))
	if len(value) > 4096 {
		value = value[len(value)-4096:]
	}
	if value == "" {
		return "command failed without output"
	}
	return value
}
