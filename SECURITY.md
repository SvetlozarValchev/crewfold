# Security

Crewfold is an owner-local collaboration service, not a sandbox.

- The daemon listens on a mode `0600` Unix socket.
- The browser binds exact IPv4 loopback, starts from a one-time URL, and exchanges
  it for an eight-hour in-memory bearer session.
- State directories are mode `0700`; uploaded documents are mode `0600`.
- Browser responses use a restrictive content security policy and reject foreign
  hosts and origins.
- Room text is untrusted data. The web client renders it as text and supports only
  a small React-based Markdown presentation for uploaded documents.
- Participant handles are bound to the exact directory from which they joined.
- A hosted steward in Crewfold's managed room directory starts Codex
  non-interactively with the owner's local permissions so it can reach the
  private Unix socket. A custom steward directory retains normal Codex trust and
  approval prompts. Starting either is an explicit owner action.

All participating agents still run as the owner and retain whatever filesystem,
network, and command authority their original host grants them. Joining a room
does not reduce or increase that authority. Do not treat Crewfold as containment,
credential isolation, or authorization against another process running under the
same operating-system account.
