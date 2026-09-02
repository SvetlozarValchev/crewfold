# Security

Crewfold is an owner-local collaboration service, not a sandbox.

- The daemon listens on an owner-only local endpoint: a mode `0600` Unix socket
  on Unix hosts or a named pipe with an owner-only Windows ACL.
- The browser binds exact IPv4 loopback, starts from a one-time URL, and exchanges
  it for an eight-hour in-memory bearer session.
- State directories are mode `0700`; uploaded documents are mode `0600`.
- Browser responses use a restrictive content security policy and reject foreign
  hosts and origins.
- Room text is untrusted data. The web client renders it as text and supports only
  a small React-based Markdown presentation for uploaded documents.
- Participant handles are bound to the exact directory from which they joined.
- A hosted steward in Crewfold's managed room directory starts the selected
  Codex or Pi runtime with the owner's local permissions so it can reach the
  private owner-local endpoint. Managed-directory startup explicitly bypasses
  that runtime's trust and approval prompts; a custom steward directory retains
  normal prompts. Starting either is an explicit owner action.
- On Windows, Crewfold does not emulate the missing Codex app-server daemon by
  scraping or typing into independently owned Codex terminals. Herdr terminal
  control is used only for the one hosted steward that Crewfold explicitly owns.

All participating agents still run as the owner and retain whatever filesystem,
network, and command authority their original host grants them. Joining a room
does not reduce or increase that authority. Do not treat Crewfold as containment,
credential isolation, or authorization against another process running under the
same operating-system account.
