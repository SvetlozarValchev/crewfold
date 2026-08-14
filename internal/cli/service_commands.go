package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"crewfold/internal/localapi"
	"crewfold/internal/service"
)

const (
	serviceResponseSchema = "urn:crewfold:schema:cli:service-response:v1"
	openResponseSchema    = "urn:crewfold:schema:cli:workbench-open-response:v1"
)

type serviceResponse struct {
	Schema   string `json:"schema"`
	Type     string `json:"type"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	UnitPath string `json:"unit_path"`
	DataDir  string `json:"data_dir"`
	Socket   string `json:"socket"`
}

type openResponse struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Origin    string `json:"origin"`
	ExpiresAt string `json:"expires_at"`
}

func (a *App) runServiceCommand(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, serviceHelp)
		return ExitOK
	}
	if len(args) != 1 {
		return a.writeFailure(mode, usageFailure("service requires exactly one lifecycle action", "run 'crewfold help service' for usage"))
	}
	paths, err := a.resolveAppDirs()
	if err != nil {
		return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "invalid_local_paths", message: err.Error(), hint: "set absolute canonical XDG directories or fix the owner home directory"})
	}
	executable, err := a.executablePath()
	if err != nil {
		return a.writeFailure(mode, internalFailure("resolve Crewfold executable", err))
	}
	manager := service.Manager{Paths: paths, Executable: executable, Run: a.runService}
	var result service.Result
	switch args[0] {
	case "install":
		result, err = manager.Install(ctx)
	case "start":
		result, err = manager.Start(ctx)
	case "stop":
		result, err = manager.Stop(ctx)
	case "status":
		result, err = manager.Status(ctx)
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown service action %q", args[0]), "use install, start, stop, or status"))
	}
	if err != nil {
		return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "service_failed", message: err.Error(), hint: "inspect the systemd user manager and retry the exact service action"})
	}
	response := serviceResponse{
		Schema: serviceResponseSchema, Type: "service", Action: result.Action, Status: result.Status,
		UnitPath: result.UnitPath, DataDir: result.DataDir, Socket: result.Socket,
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, response); err != nil {
			return a.writeFailure(outputText, internalFailure("write service result", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "Crewfold service: %s\n", response.Status)
	fmt.Fprintf(a.stdout, "action: %s\n", response.Action)
	fmt.Fprintf(a.stdout, "data: %s\n", response.DataDir)
	fmt.Fprintf(a.stdout, "socket: %s\n", response.Socket)
	fmt.Fprintf(a.stdout, "unit: %s\n", response.UnitPath)
	return ExitOK
}

func (a *App) runOpen(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, openHelp)
		return ExitOK
	}
	options, failure := parseOptions(args, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath := options["socket"]
	if socketPath == "" {
		paths, err := a.resolveAppDirs()
		if err != nil {
			return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "invalid_local_paths", message: err.Error(), hint: "set absolute canonical XDG directories or pass --socket explicitly"})
		}
		socketPath = paths.SocketPath
	}
	bootstrap, err := requestWebBootstrap(ctx, a.newClient(socketPath))
	if err != nil {
		return a.writeClientFailure(mode, "open workbench", err)
	}
	parsed, err := url.Parse(bootstrap.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || !strings.HasPrefix(parsed.Fragment, "bootstrap=") {
		return a.writeFailure(mode, internalFailure("validate workbench bootstrap", fmt.Errorf("daemon returned an invalid owner-local URL")))
	}
	if err := a.openURL(ctx, bootstrap.URL); err != nil {
		return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: "browser_open_failed", message: fmt.Sprintf("open local workbench: %v", err), hint: "verify xdg-open and the desktop browser, then retry 'crewfold open'"})
	}
	parsed.Fragment = ""
	response := openResponse{
		Schema: openResponseSchema, Type: "workbench_open", Status: "opened",
		Origin: strings.TrimSuffix(parsed.String(), "/"), ExpiresAt: bootstrap.ExpiresAt,
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, response); err != nil {
			return a.writeFailure(outputText, internalFailure("write workbench open result", err))
		}
		return ExitOK
	}
	fmt.Fprintf(a.stdout, "Crewfold workbench opened at %s\n", response.Origin)
	return ExitOK
}

func requestWebBootstrap(ctx context.Context, client daemonClient) (localapi.WebBootstrapResult, error) {
	const (
		startupWindow = 5 * time.Second
		retryInterval = 50 * time.Millisecond
	)
	deadline := time.Now().Add(startupWindow)
	for {
		result, err := client.WebBootstrap(ctx)
		if err == nil {
			return result, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return localapi.WebBootstrapResult{}, ctxErr
		}
		var networkError net.Error
		if !errors.As(err, &networkError) || time.Now().Add(retryInterval).After(deadline) {
			return localapi.WebBootstrapResult{}, err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return localapi.WebBootstrapResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}
