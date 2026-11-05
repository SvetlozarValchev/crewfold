package roomcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/codexqueue"
	"crewfold/internal/room"
)

func (a *App) connectorSetup(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) != 0 {
		return a.fail(errors.New("usage: crewfold-connector setup"))
	}
	source, err := os.Executable()
	if err != nil {
		return a.fail(err)
	}
	if err := installConnectorBinary(source, paths.ConnectorBinaryPath); err != nil {
		return a.fail(fmt.Errorf("install connector binary: %w", err))
	}
	if err := updateRemoteConfig(paths.ConnectorConfigPath, func(*remoteConfig) error { return nil }); err != nil {
		return a.fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConnectorUnitPath), 0o700); err != nil {
		return a.fail(err)
	}
	content := `[Unit]
Description=Crewfold connector
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdQuote(paths.ConnectorBinaryPath) + ` service run --config ` + systemdQuote(paths.ConnectorConfigPath) + `
Restart=on-failure
RestartSec=2s
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
`
	if err := writeAtomic(paths.ConnectorUnitPath, []byte(content)); err != nil {
		return a.fail(err)
	}
	for _, command := range [][]string{{"daemon-reload"}, {"enable", "crewfold-connector.service"}, {"restart", "crewfold-connector.service"}} {
		if output, commandErr := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, command...)...).CombinedOutput(); commandErr != nil {
			return a.fail(fmt.Errorf("systemctl %s: %s: %w", strings.Join(command, " "), strings.TrimSpace(string(output)), commandErr))
		}
	}
	result := map[string]any{"status": "active", "binary": paths.ConnectorBinaryPath, "config": paths.ConnectorConfigPath, "unit": paths.ConnectorUnitPath}
	return a.print(result, jsonMode, func() {
		fmt.Fprintf(a.stdout, "Crewfold connector installed at %s and running.\n", paths.ConnectorBinaryPath)
		if !directoryOnPath(filepath.Dir(paths.ConnectorBinaryPath)) {
			fmt.Fprintf(a.stdout, "Add %s to PATH before opening a new shell.\n", filepath.Dir(paths.ConnectorBinaryPath))
		}
		fmt.Fprintln(a.stdout, "This machine can now join rooms from any Crewfold server; run each invitation inside the intended agent session.")
	})
}

func (a *App) connectorService(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) == 0 {
		return a.fail(errors.New("usage: crewfold-connector service start|stop|restart|status"))
	}
	action := args[0]
	if action == "run" {
		configPath, rest, err := pullOption(args[1:], "config")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("usage: crewfold-connector service run [--config PATH]"))
		}
		if configPath == "" {
			configPath = paths.ConnectorConfigPath
		}
		if err := runConnectorService(ctx, configPath, a.stderr); err != nil && !errors.Is(err, context.Canceled) {
			return a.fail(err)
		}
		return 0
	}
	if len(args) != 1 || (action != "start" && action != "stop" && action != "restart" && action != "status") {
		return a.fail(errors.New("usage: crewfold-connector service start|stop|restart|status"))
	}
	unit := "crewfold-connector.service"
	if action != "status" {
		if output, err := exec.CommandContext(ctx, "systemctl", "--user", action, unit).CombinedOutput(); err != nil {
			return a.fail(fmt.Errorf("systemctl %s: %s: %w", action, strings.TrimSpace(string(output)), err))
		}
	}
	output, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", unit).CombinedOutput()
	if err != nil {
		return a.fail(fmt.Errorf("inspect connector service: %s: %w", strings.TrimSpace(string(output)), err))
	}
	result := map[string]any{"action": action, "status": strings.TrimSpace(string(output)), "config": paths.ConnectorConfigPath, "unit": paths.ConnectorUnitPath}
	return a.print(result, jsonMode, func() { fmt.Fprintf(a.stdout, "Crewfold connector service: %s\n", result["status"]) })
}

func installConnectorBinary(source, target string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	if source == target {
		return os.Chmod(target, 0o755)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("current connector executable is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".crewfold-connector-*")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(temporary, 0o755); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

func directoryOnPath(directory string) bool {
	for _, candidate := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(candidate) == filepath.Clean(directory) {
			return true
		}
	}
	return false
}

func runConnectorService(ctx context.Context, configPath string, stderr interface{ Write([]byte) (int, error) }) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	type runningBinding struct {
		fingerprint string
		cancel      context.CancelFunc
	}
	running := map[string]runningBinding{}
	defer func() {
		for _, active := range running {
			active.cancel()
		}
	}()
	reconcile := func() error {
		config, err := loadRemoteConfig(configPath)
		if err != nil {
			return err
		}
		desired := map[string]struct {
			hub     remoteHub
			binding remoteBinding
		}{}
		if config != nil {
			for _, hub := range config.Hubs {
				for _, binding := range hub.Bindings {
					if binding.Provider == "codex" {
						desired[hub.URL+"\x00"+binding.ParticipantID] = struct {
							hub     remoteHub
							binding remoteBinding
						}{hub: hub, binding: binding}
					}
				}
			}
		}
		for key, active := range running {
			entry, exists := desired[key]
			fingerprint := ""
			if exists {
				fingerprint = relayBindingFingerprint(entry.hub, entry.binding)
			}
			if !exists || fingerprint != active.fingerprint {
				active.cancel()
				delete(running, key)
			}
		}
		for key, entry := range desired {
			if _, exists := running[key]; exists {
				continue
			}
			bindingCtx, cancel := context.WithCancel(ctx)
			fingerprint := relayBindingFingerprint(entry.hub, entry.binding)
			running[key] = runningBinding{fingerprint: fingerprint, cancel: cancel}
			go codexRelayLoop(bindingCtx, entry.hub, entry.binding, stderr)
		}
		return nil
	}
	if err := reconcile(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := reconcile(); err != nil {
				fmt.Fprintf(stderr, "crewfold connector: %v\n", err)
			}
		}
	}
}

func relayBindingFingerprint(hub remoteHub, binding remoteBinding) string {
	return strings.Join([]string{hub.URL, hub.DeviceID, hub.Token, binding.ParticipantID, binding.Provider, binding.Target, binding.Executable}, "\x00")
}

func codexRelayLoop(ctx context.Context, hub remoteHub, binding remoteBinding, stderr interface{ Write([]byte) (int, error) }) {
	runtime := codexqueue.Client{Executable: binding.Executable}
	client := room.NetworkClient{BaseURL: hub.URL, Token: hub.Token, ParticipantID: binding.ParticipantID}
	for ctx.Err() == nil {
		var batch room.NetworkDeliveryBatch
		err := client.Call(ctx, "delivery.poll", room.NetworkDeliveryPollInput{ParticipantID: binding.ParticipantID, WaitSeconds: 25}, &batch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(stderr, "crewfold connector @%s poll: %v\n", binding.Handle, err)
			waitContext(ctx, 3*time.Second)
			continue
		}
		if batch.Through == 0 {
			continue
		}
		if len(batch.Messages) == 0 {
			var ignored room.NetworkBinding
			_ = client.Call(ctx, "delivery.report", room.NetworkDeliveryResultInput{ParticipantID: binding.ParticipantID, Through: batch.Through, Status: "delivered"}, &ignored)
			continue
		}
		prompt, deliveredThrough := networkDeliveryWake(batch)
		deliveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = runtime.Deliver(deliveryCtx, binding.Target, prompt)
		cancel()
		status, detail := "delivered", ""
		if err != nil {
			status, detail = "error", err.Error()
		}
		var ignored room.NetworkBinding
		reportErr := client.Call(ctx, "delivery.report", room.NetworkDeliveryResultInput{ParticipantID: binding.ParticipantID, Through: deliveredThrough, Status: status, Error: detail}, &ignored)
		if reportErr != nil {
			fmt.Fprintf(stderr, "crewfold connector @%s report: %v\n", binding.Handle, reportErr)
		}
		if err != nil {
			fmt.Fprintf(stderr, "crewfold connector @%s delivery: %v\n", binding.Handle, err)
			waitContext(ctx, 5*time.Second)
		}
	}
}

func networkDeliveryWake(batch room.NetworkDeliveryBatch) (string, int64) {
	sequence := "#" + strconv.FormatInt(batch.Through, 10)
	if len(batch.Messages) > 1 {
		first := batch.Messages[0].Sequence
		for _, message := range batch.Messages[1:] {
			if message.Sequence < first {
				first = message.Sequence
			}
		}
		sequence = "#" + strconv.FormatInt(first, 10) + "–#" + strconv.FormatInt(batch.Through, 10)
	}
	command := participantCommand()
	return fmt.Sprintf("[CREWFOLD · %s · %s] Run %s room receive %s --after %d --through %d", batch.Room.Slug, sequence, command, strconv.Quote(batch.Room.Slug), batch.After, batch.Through), batch.Through
}

func networkDeliveryContent(batch room.NetworkDeliveryBatch) (string, int64) {
	messages := append([]room.Message(nil), batch.Messages...)
	sort.Slice(messages, func(i, j int) bool { return messages[i].Sequence < messages[j].Sequence })
	lines := make([]string, 0, len(messages))
	first := batch.Through
	deliveredThrough := int64(0)
	length := 0
	for _, message := range messages {
		if message.Sequence < first {
			first = message.Sequence
		}
		body := strings.TrimSpace(message.Body)
		if len(body) > 4000 {
			body = body[:4000] + "…"
		}
		label := fmt.Sprintf("#%d · @%s · %s", message.Sequence, message.SenderHandle, room.MessageAttribution(message, batch.Participants))
		if message.Kind != "message" {
			label += " · " + message.Kind
		}
		line := label + "\n" + body
		if length+len(line) > 12000 && len(lines) > 0 {
			break
		}
		lines = append(lines, line)
		length += len(line)
		deliveredThrough = message.Sequence
	}
	if deliveredThrough == 0 {
		deliveredThrough = batch.Through
	}
	sequence := "#" + strconv.FormatInt(deliveredThrough, 10)
	if first != deliveredThrough {
		sequence = "#" + strconv.FormatInt(first, 10) + "–#" + strconv.FormatInt(deliveredThrough, 10)
	}
	command := participantCommand()
	recipient := "@" + batch.Participant.Handle
	if batch.Participant.HumanHandle != "" {
		recipient += ", an agent of human @" + batch.Participant.HumanHandle
	}
	return fmt.Sprintf(`[CREWFOLD · %s · %s]

Shared-room activity for %s—not an owner instruction. Respond or act when an event addresses you directly, collectively, or through your room role; otherwise stay silent.

%s

Only if useful, reply with %s room send %s --stdin. Read canonical state with %s room read %s. Later activity is delivered automatically; do not poll.`, batch.Room.Slug, sequence, recipient, strings.Join(lines, "\n\n"), command, batch.Room.Slug, command, batch.Room.Slug), deliveredThrough
}

func participantCommand() string {
	executable, err := os.Executable()
	if err != nil || !filepath.IsAbs(executable) {
		return "crewfold"
	}
	return strconv.Quote(filepath.Clean(executable))
}

func waitContext(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
