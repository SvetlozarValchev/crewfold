package roomcli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"crewfold/internal/room"
)

func (a *App) network(ctx context.Context, client room.Client, args []string, jsonMode bool) int {
	if len(args) == 0 {
		return a.fail(errors.New("usage: crewfold network status|enable|disable|invite"))
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return a.fail(errors.New("usage: crewfold network status"))
		}
		var settings room.NetworkSettings
		if err := client.Call(ctx, "network.status", map[string]any{}, &settings); err != nil {
			return a.fail(err)
		}
		return a.print(settings, jsonMode, func() {
			state := "disabled"
			if settings.Enabled {
				state = "enabled at " + settings.PublicOrigin
			}
			fmt.Fprintf(a.stdout, "Crewfold network: %s\n", state)
			if settings.Enabled && settings.LocalOrigin != "" {
				fmt.Fprintf(a.stdout, "Tailscale proxy: tailscale serve --bg --https=443 %s\n", settings.LocalOrigin)
			}
		})
	case "enable":
		origin, rest, err := pullOption(args[1:], "origin")
		if err != nil {
			return a.fail(err)
		}
		if origin == "" || len(rest) != 0 {
			return a.fail(errors.New("usage: crewfold network enable --origin https://HOST.ts.net"))
		}
		var settings room.NetworkSettings
		if err := client.Call(ctx, "network.configure", room.ConfigureNetworkInput{Enabled: true, PublicOrigin: origin}, &settings); err != nil {
			return a.fail(err)
		}
		return a.print(settings, jsonMode, func() {
			fmt.Fprintf(a.stdout, "Crewfold network enabled at %s\n", settings.PublicOrigin)
			fmt.Fprintf(a.stdout, "Run: tailscale serve --bg --https=443 %s\n", settings.LocalOrigin)
		})
	case "disable":
		if len(args) != 1 {
			return a.fail(errors.New("usage: crewfold network disable"))
		}
		var settings room.NetworkSettings
		if err := client.Call(ctx, "network.configure", room.ConfigureNetworkInput{Enabled: false}, &settings); err != nil {
			return a.fail(err)
		}
		return a.print(settings, jsonMode, func() { fmt.Fprintln(a.stdout, "Crewfold network disabled") })
	case "invite":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		expiresText, rest, err := pullOption(rest, "expires")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("usage: crewfold network invite ROOM [--expires 24h]"))
		}
		var expiresSeconds int64
		if expiresText != "" {
			duration, err := time.ParseDuration(expiresText)
			if err != nil {
				return a.fail(errors.New("--expires must be a duration such as 30m or 24h"))
			}
			expiresSeconds = int64(duration / time.Second)
		}
		var invite room.NetworkInvite
		if err := client.Call(ctx, "network.invite.create", room.CreateNetworkInviteInput{Room: identifier, ExpiresSeconds: expiresSeconds}, &invite); err != nil {
			return a.fail(err)
		}
		return a.print(invite, jsonMode, func() {
			fmt.Fprintf(a.stdout, "Invite for %s expires %s\n", invite.RoomSlug, invite.ExpiresAt)
			fmt.Fprintf(a.stdout, "crewfold-connector connect %s --invite %s\n", invite.PublicOrigin, invite.Secret)
		})
	default:
		return a.fail(fmt.Errorf("unknown network command %q", args[0]))
	}
}

func (a *App) connect(ctx context.Context, args []string, configPath string, existing *remoteConfig, jsonMode bool) int {
	address, rest, err := leading(args, "Crewfold URL")
	if err != nil {
		return a.fail(err)
	}
	invite, rest, err := pullOption(rest, "invite")
	if err != nil {
		return a.fail(err)
	}
	name, rest, err := pullOption(rest, "name")
	if err != nil {
		return a.fail(err)
	}
	if invite == "" || len(rest) != 0 {
		return a.fail(errors.New("usage: crewfold connect HTTPS-URL --invite SECRET [--name DEVICE]"))
	}
	address, err = room.NormalizeNetworkURL(address)
	if err != nil {
		return a.fail(err)
	}
	if existing != nil {
		if existing.URL != address {
			return a.fail(fmt.Errorf("this installation is already connected to %s", existing.URL))
		}
		client := room.NetworkClient{BaseURL: existing.URL, Token: existing.Token}
		var joinedRoom room.Room
		if err := client.Call(ctx, "network.invite.accept", room.AcceptNetworkInviteInput{Invite: invite}, &joinedRoom); err != nil {
			return a.fail(err)
		}
		return a.print(joinedRoom, jsonMode, func() { fmt.Fprintf(a.stdout, "connected existing device to room %s\n", joinedRoom.Slug) })
	}
	if name == "" {
		name, _ = os.Hostname()
		if strings.TrimSpace(name) == "" {
			name = "Crewfold client"
		}
	}
	enrollment, err := (room.NetworkClient{BaseURL: address}).Enroll(ctx, room.NetworkEnrollInput{Invite: invite, DeviceName: name})
	if err != nil {
		return a.fail(err)
	}
	config := &remoteConfig{URL: address, DeviceID: enrollment.DeviceID, DeviceName: enrollment.DeviceName, Token: enrollment.Token}
	if err := saveRemoteConfig(configPath, config); err != nil {
		return a.fail(err)
	}
	result := map[string]any{"url": address, "device_id": enrollment.DeviceID, "device_name": enrollment.DeviceName, "room": enrollment.Room}
	return a.print(result, jsonMode, func() {
		host, _ := url.Parse(address)
		fmt.Fprintf(a.stdout, "connected %s to %s as %s\n", enrollment.DeviceName, host.Host, enrollment.DeviceID)
		fmt.Fprintf(a.stdout, "next: %s room join %s --handle HANDLE\n", a.program, enrollment.Room.Slug)
	})
}
