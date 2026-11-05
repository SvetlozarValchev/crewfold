package roomcli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"crewfold/internal/appdirs"
	"crewfold/internal/room"
)

type connectorWebLink struct {
	Server    string `json:"server"`
	URL       string `json:"url"`
	Rooms     int    `json:"rooms"`
	ExpiresAt string `json:"expires_at"`
}

func (a *App) connectorOpen(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) != 0 {
		return a.fail(errors.New("usage: crewfold-connector open"))
	}
	config, err := loadRemoteConfig(paths.ConnectorConfigPath)
	if err != nil {
		return a.fail(err)
	}
	if config == nil || len(config.Hubs) == 0 {
		return a.fail(errors.New("this machine has not joined any Crewfold rooms"))
	}
	links := make([]connectorWebLink, 0, len(config.Hubs))
	for _, hub := range config.Hubs {
		var bootstrap room.NetworkBrowserBootstrap
		client := room.NetworkClient{BaseURL: hub.URL, Token: hub.Token}
		if err := client.Call(ctx, "browser.bootstrap", map[string]any{}, &bootstrap); err != nil {
			return a.fail(fmt.Errorf("open %s: %w", hub.URL, err))
		}
		parsed, err := url.Parse(bootstrap.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !strings.HasPrefix(parsed.Fragment, "network-bootstrap=") {
			return a.fail(fmt.Errorf("hub %s returned an invalid browser URL", hub.URL))
		}
		links = append(links, connectorWebLink{Server: hub.URL, URL: bootstrap.URL, Rooms: bootstrap.Rooms, ExpiresAt: bootstrap.ExpiresAt})
	}
	return a.print(links, jsonMode, func() {
		fmt.Fprintln(a.stdout, "Crewfold servers")
		for index, link := range links {
			label := "room"
			if link.Rooms != 1 {
				label = "rooms"
			}
			fmt.Fprintf(a.stdout, "\n%d. %s · %d %s\n   %s\n", index+1, link.Server, link.Rooms, label, link.URL)
		}
	})
}
