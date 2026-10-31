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
| Enter | Switch the connected daemon to the selected build |
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
./updatables/update-demo-v0.0.1 daemon status
./updatables/update-demo-v0.0.1 daemon stop
```

The CLI uses `daemoncobra` with execution-time resolvers, so `--instance` and
`--config` work before or after subcommands. `daemon status --json` prints a
machine-readable snapshot; status never starts the daemon. `daemon start` ensures
readiness, `daemon restart` restarts the selected release, and `daemon run` runs in
the foreground. Running without a subcommand opens the TUI.

On Windows, use `./updatables/update-demo-v0.0.1.exe daemon stop`.
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
Switching requires a connected Ready daemon. Press c to start or reconnect first.
The command captures the displayed Status.UpdateToken and uses Manager.UpdateIf.
If another lifecycle operation changed the execution, the UI refreshes status and
the build list and asks you to select again; it does not retry the update.

Client.Done detects connection closure without a polling RPC. Disconnection marks
readiness unknown; it does not prove the daemon stopped. Old connection notices
and command results cannot overwrite newer UI state. Closing the UI cancels its
connection watcher and in-flight command; RPCs are never automatically replayed.

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

## Configuration

The demo reads user-level `daemonkit-demo/config.toml` from the platform config
directory. `--config /absolute/file.toml` selects an explicit file (it must exist
when loaded). The path is stored in the managed daemon arguments when that release
is selected; another CLI does not change an existing daemon's configuration path.
All demo instances use the same default config file unless given explicit paths.

```toml
[daemon]
greeting = "Hello" # Used by the v2+ greeting RPC.
```

```sh
./updatables/update-demo-v0.0.1 config path
./updatables/update-demo-v0.0.1 config set daemon.greeting '"Welcome"'
./updatables/update-demo-v0.0.1 config get daemon.greeting
./updatables/update-demo-v0.0.1 config validate
./updatables/update-demo-v0.0.1 config edit
./updatables/update-demo-v0.0.1 config reload
```

Set preserves surrounding comments and formatting. Edit uses VISUAL, EDITOR, or
the platform editor with a temporary file. Failed edits retain a recovery draft;
concurrent saves use last-save-wins. Get shows the file value, not the applied value
or defaults. Neither editing command starts the daemon or applies runtime state.

Run v2 or v3 and press **2** to observe the greeting before and after Reload.
Reload connects to an existing daemon, verifies its fixed config path matches the
CLI target, and publishes validated state. A failed reload retains the last applied
greeting. Restart/update loads disk again; no last-applied snapshot is persisted.

`config validate` checks the file or, if the optional default file is missing,
the defaults. `config path` and daemon inspection remain available with malformed
TOML. `config edit` can repair it without parsing before opening the editor.
