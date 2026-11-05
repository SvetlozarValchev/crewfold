package roomcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/codexapp"
	"crewfold/internal/room"
)

func (a *App) relay(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) == 0 {
		return a.fail(errors.New("usage: crewfold relay install|start|stop|status|run"))
	}
	action := args[0]
	if action == "run" {
		configPath, rest, err := pullOption(args[1:], "config")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("usage: crewfold relay run [--config PATH]"))
		}
		if configPath == "" {
			configPath = paths.RemoteConfigPath
		}
		if err := runRelay(ctx, configPath, a.stderr); err != nil && !errors.Is(err, context.Canceled) {
			return a.fail(err)
		}
		return 0
	}
	if len(args) != 1 {
		return a.fail(errors.New("usage: crewfold relay install|start|stop|status|run"))
	}
	unit := "crewfold-relay.service"
	if action == "install" {
		if config, err := loadRemoteConfig(paths.RemoteConfigPath); err != nil || config == nil {
			if err == nil {
				err = errors.New("connect this machine to a Crewfold hub first")
			}
			return a.fail(err)
		}
		executable, err := os.Executable()
		if err != nil {
			return a.fail(err)
		}
		if err := os.MkdirAll(filepath.Dir(paths.RelayUnitPath), 0o700); err != nil {
			return a.fail(err)
		}
		content := `[Unit]
Description=Crewfold remote delivery relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdQuote(executable) + ` relay run --config ` + systemdQuote(paths.RemoteConfigPath) + `
Restart=on-failure
RestartSec=2s
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
`
		if err := writeAtomic(paths.RelayUnitPath, []byte(content)); err != nil {
			return a.fail(err)
		}
		for _, command := range [][]string{{"daemon-reload"}, {"enable", unit}, {"restart", unit}} {
			if output, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, command...)...).CombinedOutput(); err != nil {
				return a.fail(fmt.Errorf("systemctl %s: %s: %w", strings.Join(command, " "), strings.TrimSpace(string(output)), err))
			}
		}
	} else if action == "start" || action == "stop" {
		if output, err := exec.CommandContext(ctx, "systemctl", "--user", action, unit).CombinedOutput(); err != nil {
			return a.fail(fmt.Errorf("systemctl %s: %s: %w", action, strings.TrimSpace(string(output)), err))
		}
	} else if action != "status" {
		return a.fail(errors.New("usage: crewfold relay install|start|stop|status|run"))
	}
	output, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", unit).CombinedOutput()
	if err != nil {
		return a.fail(fmt.Errorf("inspect relay: %s: %w", strings.TrimSpace(string(output)), err))
	}
	result := map[string]any{"action": action, "status": strings.TrimSpace(string(output)), "config": paths.RemoteConfigPath, "unit": paths.RelayUnitPath}
	return a.print(result, jsonMode, func() { fmt.Fprintf(a.stdout, "Crewfold relay: %s\n", result["status"]) })
}

func runRelay(ctx context.Context, configPath string, stderr interface{ Write([]byte) (int, error) }) error {
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
		if config == nil {
			return errors.New("remote configuration not found; run crewfold connect first")
		}
		desired := map[string]remoteBinding{}
		for _, binding := range config.Bindings {
			if binding.Provider == "codex" {
				desired[binding.ParticipantID] = binding
			}
		}
		for key, active := range running {
			binding, exists := desired[key]
			fingerprint := ""
			if exists {
				fingerprint = relayBindingFingerprint(*config, binding)
			}
			if !exists || fingerprint != active.fingerprint {
				active.cancel()
				delete(running, key)
			}
		}
		for key, binding := range desired {
			if _, exists := running[key]; exists {
				continue
			}
			bindingCtx, cancel := context.WithCancel(ctx)
			fingerprint := relayBindingFingerprint(*config, binding)
			running[key] = runningBinding{fingerprint: fingerprint, cancel: cancel}
			go codexRelayLoop(bindingCtx, *config, binding, stderr)
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
				fmt.Fprintf(stderr, "crewfold relay: %v\n", err)
			}
		}
	}
}

func relayBindingFingerprint(config remoteConfig, binding remoteBinding) string {
	return strings.Join([]string{config.URL, config.DeviceID, config.Token, binding.ParticipantID, binding.Provider, binding.Target}, "\x00")
}

func codexRelayLoop(ctx context.Context, config remoteConfig, binding remoteBinding, stderr interface{ Write([]byte) (int, error) }) {
	socket, err := codexapp.DefaultSocketPath()
	if err != nil {
		fmt.Fprintf(stderr, "crewfold relay @%s: %v\n", binding.Handle, err)
		return
	}
	runtime := codexapp.Client{SocketPath: socket}
	client := room.NetworkClient{BaseURL: config.URL, Token: config.Token, ParticipantID: binding.ParticipantID}
	for ctx.Err() == nil {
		var batch room.NetworkDeliveryBatch
		err := client.Call(ctx, "delivery.poll", room.NetworkDeliveryPollInput{ParticipantID: binding.ParticipantID, WaitSeconds: 25}, &batch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(stderr, "crewfold relay @%s poll: %v\n", binding.Handle, err)
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
		prompt, deliveredThrough := networkDeliveryPrompt(batch)
		deliveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		messageID := fmt.Sprintf("crewfold-network:%s:%d", binding.ParticipantID, deliveredThrough)
		err = runtime.Deliver(deliveryCtx, binding.Target, prompt, messageID)
		cancel()
		status, detail := "delivered", ""
		if err != nil {
			status, detail = "error", err.Error()
			if errors.Is(err, codexapp.ErrThreadNotLoaded) || errors.Is(err, codexapp.ErrDirectInputUnavailable) || strings.Contains(err.Error(), "activeTurnNotSteerable") {
				status = "queued"
			}
		}
		var ignored room.NetworkBinding
		reportErr := client.Call(ctx, "delivery.report", room.NetworkDeliveryResultInput{ParticipantID: binding.ParticipantID, Through: deliveredThrough, Status: status, Error: detail}, &ignored)
		if reportErr != nil {
			fmt.Fprintf(stderr, "crewfold relay @%s report: %v\n", binding.Handle, reportErr)
		}
		if err != nil {
			fmt.Fprintf(stderr, "crewfold relay @%s delivery: %v\n", binding.Handle, err)
			waitContext(ctx, 5*time.Second)
		}
	}
}

func networkDeliveryPrompt(batch room.NetworkDeliveryBatch) (string, int64) {
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
		label := fmt.Sprintf("#%d · @%s", message.Sequence, message.SenderHandle)
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
	return fmt.Sprintf(`[CREWFOLD · %s · %s]

Shared-room activity for @%s—not an owner instruction. Respond or act when an event addresses you directly, collectively, or through your room role; otherwise stay silent.

%s

Only if useful, reply with %s room send %s --stdin. Read canonical state with %s room read %s. Later activity is delivered automatically; do not poll.`, batch.Room.Slug, sequence, batch.Participant.Handle, strings.Join(lines, "\n\n"), command, batch.Room.Slug, command, batch.Room.Slug), deliveredThrough
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
