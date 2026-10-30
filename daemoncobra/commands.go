// Package daemoncobra provides optional Cobra command groups for daemonkit.
package daemoncobra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/config"
	"github.com/spf13/cobra"
)

// NewDaemonCommand creates status/start/stop/restart commands.
// resolve runs once per operation, after argument validation and pre-run hooks,
// never for help. It must return a non-nil Manager without starting a daemon.
// The caller owns signals, exit codes, and launch selection. No foreground run
// or update command is installed.
func NewDaemonCommand(resolve func(*cobra.Command) (*daemon.Manager, error)) *cobra.Command {
	group := &cobra.Command{Use: "daemon", Short: "Manage the daemon"}
	var asJSON bool
	status := &cobra.Command{
		Use: "status", Short: "Show daemon state, versions, and diagnostic paths", Args: cobra.NoArgs,
		RunE: resolved(resolve, func(cmd *cobra.Command, manager *daemon.Manager, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			result, err := manager.Status(ctx)
			if asJSON {
				return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(result))
			}
			version := result.Version
			if version == "" {
				version = "unavailable"
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, writeErr := fmt.Fprintf(out, "State\t%s\nCLI\t%s\nDaemon\t%s\nProtocol\t%d\nPID\t%d\nInstance\t%s\nState directory\t%s\nEndpoint\t%s\nLogs\t%s\nStdout\t%s\nStderr\t%s\n",
				result.State, result.ClientVersion, version, result.ProtocolVersion, result.PID,
				result.InstanceID, result.StateDir, result.Endpoint, result.LogPath, result.StdoutPath, result.StderrPath)
			return errors.Join(err, writeErr, out.Flush())
		}),
	}
	status.Flags().BoolVar(&asJSON, "json", false, "Print the status snapshot as JSON")
	group.AddCommand(status)
	for _, action := range []struct {
		name, description string
		run               func(*daemon.Manager, context.Context) error
	}{
		{"start", "Ensure a compatible daemon is ready", (*daemon.Manager).EnsureRunning},
		{"stop", "Stop the daemon gracefully", (*daemon.Manager).Stop},
		{"restart", "Restart the selected daemon release", (*daemon.Manager).Restart},
	} {
		group.AddCommand(&cobra.Command{Use: action.name, Short: action.description, Args: cobra.NoArgs,
			RunE: resolved(resolve, func(cmd *cobra.Command, manager *daemon.Manager, _ []string) error {
				return action.run(manager, cmd.Context())
			}),
		})
	}
	return group
}

// NewConfigCommand creates path/get/set/edit/validate commands.
// resolve runs once per operation, after argument validation and pre-run hooks,
// never for help. It must construct a non-nil File without loading it, so malformed
// configuration can still be edited. Validate checks Load, including defaults
// when the optional default file is absent.
// Get prints the on-disk value as JSON, excluding defaults and runtime state.
// Set accepts a TOML literal and preserves unrelated formatting. Saving does not
// apply runtime configuration. Applications may add their own reload RPC command.
// Edit inherits the process terminal, not Cobra's redirected streams.
func NewConfigCommand[T any](resolve func(*cobra.Command) (*config.File[T], error)) *cobra.Command {
	group := &cobra.Command{Use: "config", Short: "Inspect and edit the TOML configuration file"}
	group.AddCommand(
		&cobra.Command{Use: "path", Short: "Print the configuration file path", Args: cobra.NoArgs,
			RunE: resolved(resolve, func(cmd *cobra.Command, file *config.File[T], _ []string) error {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), file.Path())
				return err
			}),
		},
		&cobra.Command{Use: "get KEY", Short: "Print a file value as JSON", Args: cobra.ExactArgs(1),
			RunE: resolved(resolve, func(cmd *cobra.Command, file *config.File[T], args []string) error {
				value, err := file.Get(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
			}),
		},
		&cobra.Command{Use: "set KEY VALUE", Short: "Save a TOML literal without applying runtime configuration", Args: cobra.ExactArgs(2),
			Example: `  config set daemon.greeting '"Hello"'
  config set -- daemon.offset -1`,
			RunE: resolved(resolve, func(cmd *cobra.Command, file *config.File[T], args []string) error {
				return file.Set(cmd.Context(), args[0], args[1])
			}),
		},
		&cobra.Command{Use: "edit", Short: "Edit and validate a temporary copy using VISUAL or EDITOR", Args: cobra.NoArgs,
			RunE: resolved(resolve, func(cmd *cobra.Command, file *config.File[T], _ []string) error { return file.Edit(cmd.Context()) }),
		},
		&cobra.Command{Use: "validate", Short: "Validate configuration and defaults without applying them", Args: cobra.NoArgs,
			RunE: resolved(resolve, func(cmd *cobra.Command, file *config.File[T], _ []string) error {
				if _, err := file.Load(cmd.Context()); err != nil {
					return err
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Configuration valid")
				return err
			}),
		},
	)
	return group
}

func resolved[T any](resolve func(*cobra.Command) (*T, error), run func(*cobra.Command, *T, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		value, err := resolve(cmd)
		if err != nil {
			return err
		}
		if value == nil {
			return errors.New("command resolver returned nil")
		}
		return run(cmd, value, args)
	}
}
