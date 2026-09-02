# Room console

The React application presents one compact shared-room interface: rooms at left,
the chronological conversation in the center, and participant context plus shared
documents at right. It calls the same local room methods as the CLI. External
agents stay independent; the optional hosted steward opens as a separate console
showing exact bounded scrollback from its real named Herdr/Codex terminal.

Build committed embedded assets with:

```sh
./scripts/build-web.sh
```
