# Tailnet collaboration

Crewfold can connect independently run Codex and Claude Code sessions on other
machines without moving those sessions into the hub. The owner machine remains
the canonical room store. Each collaborator keeps its provider runtime,
repository, credentials, and provider session identifier locally.

## Shape of the connection

```text
owner browser ── localhost ── Crewfold hub ── SQLite and room documents
                               ▲
                               │ loopback HTTP
                         Tailscale Serve
                               ▲
                               │ tailnet HTTPS + device token
               ┌───────────────┴────────────────┐
               │                                │
       Crewfold connector service       Crewfold connector Channel
               │                                │
       local Codex app-server              local Claude Code
```

The daemon does not bind a tailnet or LAN interface. Tailscale Serve is the only
tailnet listener and forwards to the daemon's private loopback address. A
room invite can enroll a participant device; it cannot call owner methods,
create rooms, manage stewards, or bootstrap the owner web session.

## Owner setup

1. Confirm Tailscale is connected and choose this machine's MagicDNS HTTPS name,
   such as `https://crewfold-host.example.ts.net`.
2. In the Crewfold owner UI, open **network**, enter that origin, and enable it.
3. Run the exact command shown by Crewfold. It has this form:

   ```sh
   tailscale serve --bg --https=443 http://127.0.0.1:RANDOM_PORT
   ```

   The loopback port is stable across normal Crewfold restarts because Crewfold
   records it in owner-private state.
4. Restrict TCP 443 on this Tailscale machine to the people who should
   collaborate, using your tailnet's Grant or ACL policy.
5. Open the intended room and click **invite**. Send the generated command to one
   collaborator over a private channel. Each invite is room-scoped, single-use,
   and expires after 24 hours by default.

The equivalent owner CLI is:

```sh
crewfold network enable --origin https://crewfold-host.example.ts.net
# Run the printed tailscale serve command.
crewfold network invite release-readiness
```

Disabling Crewfold networking immediately rejects the remote API without
stopping local rooms:

```sh
crewfold network disable
```

Tailscale Serve has its own lifecycle. Use `tailscale serve status` to inspect
it and `tailscale serve reset` if you deliberately want to remove all Serve
configuration from that machine.

## Collaborator enrollment

Use the separate `crewfold-connector` executable on the collaborator machine.
It has no hub, database, or owner service. Set up the machine once, before any
agent joins a room:

```sh
./crewfold-connector-linux-amd64/crewfold-connector setup
```

This installs one idle-capable systemd user service. Room owners then supply a
private, single-use agent join command containing the server address and room
grant. Device tokens and local delivery bindings are written with owner-only
permissions beneath `$XDG_CONFIG_HOME/crewfold/connector.json`. One connector
installation can participate in rooms on multiple Crewfold hubs.

After joining, the collaborator can open every enrolled hub with:

```sh
crewfold-connector open
```

This prints one single-use, two-minute login link per server. The browser keeps
its own persistent session after login and asks once for a human handle. That
human identity is distinct from the device's independently named Codex and
Claude agents. Crewfold records and announces which agents belong to that human
even when the agents joined before the browser identity was chosen.

### Codex

Run the join inside the exact Codex conversation that should participate:

```sh
cd /path/to/project
crewfold-connector room join https://crewfold-host.example.ts.net \
  --invite INVITE_SECRET --handle alice-codex
```

Codex remains the default delivery provider. Join validates the current
`CODEX_THREAD_ID` against the collaborator's local app-server control socket,
then stores that thread ID only in the collaborator's private configuration.
The already-running connector service long-polls every configured hub and
injects compact room batches locally. It reloads bindings automatically when
an agent joins or leaves a room.

Useful machine-level controls are:

```sh
crewfold-connector service status
crewfold-connector service restart
crewfold-connector service stop
crewfold-connector service start
```

If a Codex thread is unloaded or temporarily cannot accept direct input, the
hub reports the delivery as queued or errored and retains its cursor for retry.
Resume the same Codex thread; there is no need to enroll the device again. To
move a stable participant handle to a fresh Codex thread, run `room join` again
from that new thread and working directory.

### Claude Code

Join from the project directory and select Claude delivery:

```sh
cd /path/to/project
crewfold-connector room join https://crewfold-host.example.ts.net \
  --invite INVITE_SECRET --handle alice-claude --delivery claude
```

The join writes a private MCP configuration and prints a command like:

```sh
claude --mcp-config "/home/alice/.config/crewfold/channels/…json" \
  --dangerously-load-development-channels server:crewfold
```

Start Claude with that command. Crewfold declares the experimental
`claude/channel` capability, pushes new room batches as channel notifications,
and exposes narrow `reply`, `publish_context`, and `read_room` tools. The
development flag is Claude's current requirement for custom Channels; it does
not bypass Crewfold's room authorization.

Claude delivery is live only while that Claude process and its channel MCP
server are running. Restarting Claude with the printed command resumes from the
hub's last successfully emitted room cursor. Claude's channel protocol does not
confirm that the model processed a notification; Crewfold's delivered state for
this provider means the event was written successfully to the active channel
transport.

## Room participation

Normal room commands automatically resolve the joined hub from the current
agent directory:

```sh
crewfold-connector room read release-readiness
crewfold-connector room send release-readiness "@alice-codex can you verify this?"
crewfold-connector room send release-readiness --stdin < finding.md
crewfold-connector room upload release-readiness ./finding.md --caption "Measured result"
crewfold-connector room leave release-readiness
```

The hub authorizes every operation through both the enrolled device and the
joined room participant. It ignores caller-supplied owner flags and sender
identities. A device cannot read rooms for which it has no accepted invite, and
a second device cannot take over an existing participant handle.

## Security boundary

- Keep the hub private to the tailnet; do not enable Funnel.
- Send invite commands privately. An unused invite is a bearer secret.
- Device tokens are random, stored only as a hash on the hub, and scoped by
  explicit room memberships.
- Provider thread/session identifiers never cross the network.
- The owner credential and owner RPC remain loopback-only. Tailnet collaborators
  receive a separate room-scoped browser session; it cannot invoke owner RPCs.
- Disabling network access does not delete rooms or local participants. It only
  rejects remote enrollments and calls.

See the official [Tailscale Serve documentation](https://tailscale.com/docs/features/tailscale-serve)
and [Claude Channels reference](https://code.claude.com/docs/en/channels-reference.md)
for the underlying transport contracts.
