package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/config"
	"github.com/134130/go-daemonkit/daemoncobra"
	"github.com/spf13/cobra"
)

// Set with go build -ldflags "-X main.version=0.0.2".
var version = "0.0.1"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return newRoot().ExecuteContext(ctx)
}

func newRoot() *cobra.Command {
	var buildDir, instance, configPath string
	root := &cobra.Command{Use: "update-demo", Short: "Explore daemon versions and typed RPC", Version: version,
		SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if version != "0.0.1" && version != "0.0.2" && version != "0.0.3" {
				return fmt.Errorf("unsupported demo build version %q", version)
			}
			return nil
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentFlags().StringVar(&buildDir, "builds", "updatables", "directory containing locally built executables")
	root.PersistentFlags().StringVar(&instance, "instance", "default", "isolated demo instance")
	root.PersistentFlags().StringVar(&configPath, "config", "", "explicit TOML config file (default: user config directory)")
	options := func() daemon.Options {
		return daemon.Options{AppID: "daemonkit-demo", InstanceID: instance, Version: version}
	}
	resolveFile := func(*cobra.Command) (*config.File[demoConfig], error) { return configFile(options(), configPath) }
	resolveManager := func(cmd *cobra.Command) (*daemon.Manager, error) {
		args := []string{"daemon", "run", "--instance", instance}
		if configPath != "" {
			file, err := resolveFile(cmd)
			if err != nil {
				return nil, err
			}
			args = append(args, "--config", file.Path())
		}
		return daemon.NewManager(options(), args...)
	}
	control := daemoncobra.NewDaemonCommand(resolveManager)
	control.AddCommand(&cobra.Command{Use: "run", Short: "Run the daemon in the foreground", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runDaemon(cmd.Context(), options(), configPath) },
	})
	configuration := daemoncobra.NewConfigCommand(resolveFile)
	configuration.AddCommand(&cobra.Command{Use: "reload", Short: "Apply the configuration to the running daemon", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := resolveFile(cmd)
			if err != nil {
				return err
			}
			return reloadConfig(cmd.Context(), options(), file.Path(), cmd.OutOrStdout())
		},
	})
	root.AddCommand(control, configuration)
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		manager, err := resolveManager(cmd)
		if err != nil {
			return err
		}
		builds, err := discoverBuilds(buildDir)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(cmd.Context())
		session := &session{manager: manager, options: options()}
		// Cancel in-flight commands before closing the shared client.
		defer func() { cancel(); session.close() }()
		_, err = tea.NewProgram(newModel(ctx, session, builds), tea.WithContext(ctx)).Run()
		if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
			return nil
		}
		return err
	}
	return root
}
