# Daemon Update Demo

Build three releases, switch the running daemon between them, and call RPCs from
the original v1 CLI. The TUI uses Bubble Tea v2, Bubbles v2, and Lip Gloss v2.
UI dependencies live in this example's module, outside the framework module.

## Run

From this directory, with mise installed:

```sh
mise trust
mise run build
mise run run
```

The build task creates native executables under `updatables/`:

- `update-demo-v0.0.1`
- `update-demo-v0.0.2`
- `update-demo-v0.0.3`

Windows filenames have an `.exe` suffix. Versions are embedded with
`go build -ldflags "-X main.version=0.0.1"`, not build tags.
The run task also builds these artifacts and opens the v1 CLI.

The demo uses app ID `daemonkit-demo` and instance `default`. It connects to an
existing daemon or starts the previously selected release. A fresh instance
starts v1; reopening the TUI does not reset the daemon to v1.

## Try the full cycle

1. Select v1 with the arrow keys and press Enter.
2. Press `1`: the current-time RPC succeeds. Press `2` or `3`: Method not found.
3. Select v2 and press Enter. The daemon version and PID change; the CLI stays v1.
4. Press `2`: the greeting RPC now succeeds.
5. Switch to v3 and press `3` to see its Go version, OS, and architecture.
6. Switch back to v1. Calls `2` and `3` return Method not found again.

All CLI builds know the three DTO contracts. Daemons register only the handlers
supported by their build version. There is no dynamic API discovery.

| Key | Action |
| --- | --- |
| ↑ / ↓ | Select a local build |
| Enter | Switch to the selected build; start it if stopped |
| 1 | `demo.now` — v1+ |
| 2 | `demo.greet` — v2+ |
| 3 | `demo.runtime` — v3+ |
| r | Call `demo.now` using the raw Call API |
| w | Subscribe to `demo.tick`, receive one event, then unsubscribe |
| s | Stop the daemon gracefully |
| c | Connect or start the previously selected release |
| PgUp / PgDn | Scroll activity |
| Tab | Toggle the build panel |
| q / Ctrl+C | Exit the TUI, leaving the daemon running |

Only one operation runs at a time. The activity panel retains the last 200 entries.
The clock worker logs and publishes the current time every five seconds;
Shutdown cancels and joins that worker. A standard logger is explicitly injected
using `log.New(app.LogWriter(), ...)`. Its logs are in the instance's
`logs/daemon.log`, under the platform state directory. The framework drains
accepted messages after Shutdown and rotates the file. Direct stdout/stderr
are captured separately in `logs/daemon.stdout` and `logs/daemon.stderr`.
The status command prints all three paths.

To inspect runtime information and paths, or stop outside the TUI:

```sh
./updatables/update-demo-v0.0.1 status
./updatables/update-demo-v0.0.1 stop
```

On Windows, use `./updatables/update-demo-v0.0.1.exe stop`.
For an independent instance, launch with `--instance another`; use the same flag
when stopping it. Use `--builds DIR` to read builds from another directory.

## Update behavior

The list recognizes these three exact native filenames at startup. It never
executes candidates to inspect them. Use only binaries you built and trust:
filenames are labels, not proof of authenticity or embedded version. After
switching, the UI obtains the actual daemon version, PID, and readiness through
`Manager.Status(ctx)`. No application-specific status RPC is needed.

The framework copies the candidate into managed release storage, stops the old
daemon, verifies the replacement, and rolls back if replacement startup fails.
The UI reconnects after success or failure and reports the resulting daemon.
When stopped, the UI selects the candidate and then starts it; there is no
previous running process to restore if that start fails.

The CLI executable is not replaced. Rebuild only while the TUI is closed:
Windows locks the running CLI executable. Closing during a switch cancels the
wait; the next lifecycle operation resumes any pending update transaction.
Application data is not rolled back.

## Code

- `api/`: explicit shared RPC and event contracts.
- `service.go`, `clock.go`: version-dependent handlers and worker lifecycle.
- `session.go`: lifecycle operations, RPC calls, and reconnects.
- `tui.go`, `components.go`: input and rendering. Parents pass Size constraints;
  View is read-only and Update synchronizes viewport dimensions.
- `builds.go`: local artifact discovery.

Run `mise run test` for layout checks, a synctest clock lifecycle test, and a real
process test that builds all versions and exercises upgrade, downgrade, RPC
availability, events, and restarting from the stopped state.
