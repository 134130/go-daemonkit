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

// NewDaemonCommand creates status/start/stop/restart commands for a fixed manager.
// Construction performs no I/O. The caller owns signals, exit codes, and manager
// selection; construct after resolving any instance flags. No foreground run or
// update command is installed. Commands inherit the executing Cobra context.
func NewDaemonCommand(manager *daemon.Manager) *cobra.Command {
	group := &cobra.Command{Use: "daemon", Short: "Manage the daemon"}
	var asJSON bool
	status := &cobra.Command{
		Use: "status", Short: "Show daemon state, versions, and diagnostic paths", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
		},
	}
	status.Flags().BoolVar(&asJSON, "json", false, "Print the status snapshot as JSON")
	group.AddCommand(status)
	for _, action := range []struct {
		name, description string
		run               func(context.Context) error
	}{
		{"start", "Ensure a compatible daemon is ready", manager.EnsureRunning},
		{"stop", "Stop the daemon gracefully", manager.Stop},
		{"restart", "Restart the selected daemon release", manager.Restart},
	} {
		group.AddCommand(&cobra.Command{Use: action.name, Short: action.description, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return action.run(cmd.Context()) },
		})
	}
	return group
}

// NewConfigCommand creates path/get/set/edit commands for a fixed file.
// Get prints the on-disk value as JSON, excluding defaults and runtime state.
// Set accepts a TOML literal and preserves unrelated formatting. Saving does not
// apply runtime configuration. Applications may add their own reload RPC command.
// Edit inherits the process terminal, not Cobra's redirected streams.
func NewConfigCommand[T any](file *config.File[T]) *cobra.Command {
	group := &cobra.Command{Use: "config", Short: "Inspect and edit the TOML configuration file"}
	group.AddCommand(
		&cobra.Command{Use: "path", Short: "Print the configuration file path", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), file.Path())
				return err
			},
		},
		&cobra.Command{Use: "get KEY", Short: "Print a file value as JSON", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				value, err := file.Get(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
			},
		},
		&cobra.Command{Use: "set KEY VALUE", Short: "Save a TOML literal without applying runtime configuration", Args: cobra.ExactArgs(2),
			Example: `  config set daemon.greeting '"Hello"'
  config set -- daemon.offset -1`,
			RunE: func(cmd *cobra.Command, args []string) error {
				return file.Set(cmd.Context(), args[0], args[1])
			},
		},
		&cobra.Command{Use: "edit", Short: "Edit and validate a temporary copy using VISUAL or EDITOR", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return file.Edit(cmd.Context()) },
		},
	)
	return group
}
