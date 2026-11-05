package roomcli

import (
	"context"
	"errors"
	"fmt"
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
			fmt.Fprintf(a.stdout, "crewfold-connector room join %s --invite %s --handle HANDLE\n", invite.PublicOrigin, invite.Secret)
		})
	default:
		return a.fail(fmt.Errorf("unknown network command %q", args[0]))
	}
}
