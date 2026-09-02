# Platform support plan

Crewfold should provide the same room, document, delivery, steward, and web
behavior across supported desktop operating systems. Platform-specific code is a
boundary around the shared product rather than a fork of it.

## Target platforms

| Platform | Initial support target | Native integration |
| --- | --- | --- |
| Linux | Preserve full support | XDG directories, Unix socket, systemd user service, `xdg-open` |
| Windows | Add full core support | Known Folders, named pipe, per-user Startup launcher, default browser |
| macOS | Add full core support | Library directories, Unix socket, launchd user agent, `open` |

Core support means the daemon, CLI, SQLite store, document storage, loopback web
console, and manual participants work natively. Codex delivery and optional
Herdr stewards additionally depend on their selected agent runtime exposing a
compatible native integration. Herdr stewardship with Codex or Pi works on
native Windows; direct Codex thread delivery does not. An unavailable optional
integration must not prevent the core daemon from starting.

## Design principles

1. Keep room and web behavior platform-neutral.
2. Put operating-system decisions behind narrow interfaces or build-tagged
   files; do not scatter runtime OS checks through business logic.
3. Preserve owner-local authentication and filesystem permissions on every
   platform.
4. Preserve existing Linux CLI behavior where practical.
5. Prefer native host operation. WSL is a Linux test environment, not the
   Windows implementation or a runtime requirement.
6. Keep `crewfold daemon run` available on every platform independently of
   background-service integration.

## Platform boundaries

### Owner directories

The path resolver should return semantic locations rather than expose XDG as the
cross-platform model:

- durable state and SQLite data;
- configuration;
- transient runtime data;
- the local IPC endpoint;
- the platform service definition, when applicable.

Defaults:

- Linux continues to use XDG state, config, and runtime roots.
- Windows uses per-user Known Folder locations, normally under
  `%LOCALAPPDATA%`.
- macOS uses the appropriate per-user `~/Library` locations.

Environment overrides remain useful for tests and isolated daemon instances.
Platform path validation must account for Windows volumes, UNC paths, reserved
names, and case-insensitivity without weakening Linux validation.

### Owner-local IPC

The room protocol remains newline-delimited JSON, but the transport becomes an
endpoint abstraction:

- Unix-domain sockets on Linux and macOS;
- named pipes with an owner-only ACL on Windows.

Client and server code should depend on `Dial` and `Listen` helpers instead of
calling `net.Dial("unix", ...)` and `net.Listen("unix", ...)` directly. Endpoint
parsing should support an explicit transport form while retaining the existing
`--socket` and `CREWFOLD_SOCKET` behavior on Linux during the transition.

Codex app-server control is a separate transport boundary. Its Windows endpoint
must be discovered from the native Codex installation rather than assumed to be
the same as Crewfold IPC.

### Background service

Service commands should delegate to a platform implementation:

- Linux: current systemd user unit;
- Windows: a per-user Startup-folder launcher that requires no elevation;
- macOS: launchd user agent.

The shared CLI owns action validation and output shape. Platform implementations
own definition generation, installation, start, stop, and status inspection.

### Desktop integration

Opening the web console should use a platform helper:

- Linux: `xdg-open`;
- Windows: the shell/default-browser API;
- macOS: `open`.

Signal registration and any process-control differences belong in build-tagged
files. Shared shutdown continues to use context cancellation.

### Optional runtimes

Codex delivery and Herdr stewardship report capability errors at the operation
that needs them. Starting the core daemon does not require either binary or
endpoint to exist. Native Windows Herdr/Pi startup uses a private `pi.cmd` shim
directory because PowerShell otherwise selects npm's non-Win32 extensionless
launcher. The user's Pi installation is not modified.

## Delivery phases

### Phase 0: baseline and compile matrix

- Establish a clean Linux test run in WSL.
- Install the pinned Go and pnpm toolchains in both development environments.
- Add Windows and Linux compile/test jobs; add macOS compile coverage when CI is
  introduced.
- Record every direct OS assumption and classify it by boundary.

### Phase 1: low-risk platform seams

- Split signal handling by platform.
- Add a cross-platform browser opener.
- Refactor directory resolution into shared semantics plus OS defaults.
- Make build and check entry points usable from PowerShell as well as POSIX
  shells.

### Phase 2: IPC abstraction

- Extract the current Unix listener and dialer without changing the protocol.
- Add Windows named-pipe listener/dialer and owner-only access controls.
- Generalize user-facing endpoint naming while preserving Linux compatibility.
- Run the full room workflow against each native transport.

### Phase 3: service lifecycle

- Extract the existing systemd implementation.
- Implement non-elevated Windows install/start/stop/status behavior with a
  transparent Startup-folder command launcher.
- Add isolated lifecycle tests and manual reboot/login validation.
- Add launchd user-agent lifecycle support.

### Phase 4: external integrations

- Discover and test the native Windows Codex app-server endpoint.
- Verify thread inspection and delivery on both Linux and Windows.
- Determine Herdr's native platform support and adapt process/session handling
  where necessary.
- Document capability limitations without coupling core room operation to them.

### Phase 5: packaging and release gate

- Produce Linux archives and Windows zip packages from one versioned build.
- Add install/update instructions per platform.
- Run native smoke tests for daemon startup, room workflow, web bootstrap,
  restart persistence, and access isolation.

## Test environments

Development uses two independent checkouts to avoid sharing platform-specific
build artifacts and `node_modules` trees:

- native Windows: `C:\Users\colin\crewfold`;
- WSL2 Arch Linux: `/home/colin/src/crewfold`.

Every shared behavior change must pass on Windows and Linux. Linux-specific
systemd and Unix-socket tests run in WSL, whose systemd user session is enabled.
Windows IPC and service tests run from native Windows. Cross-compilation is a
useful early check but does not replace native transport, ACL, browser, or
lifecycle tests.

## Remaining validation

1. Native Codex app-server thread delivery remains unavailable because Codex
   reports that daemon lifecycle support is Unix-only.
2. Herdr stewardship with Codex and Pi has passed isolated native Windows
   lifecycle probes, including startup gates, onboarding, room publication,
   stop, session deletion, and temporary integration cleanup.
3. Perform a manual launchd lifecycle smoke test on native macOS before the
   first release that advertises packaged macOS support.
