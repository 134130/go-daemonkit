package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	daemon "github.com/134130/go-daemonkit"
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
	buildDir := flag.String("builds", "updatables", "directory containing locally built executables")
	instance := flag.String("instance", "default", "isolated demo instance")
	showVersion := flag.Bool("version", false, "print this CLI's build version")
	flag.Parse()
	if version != "0.0.1" && version != "0.0.2" && version != "0.0.3" {
		return fmt.Errorf("unsupported demo build version %q", version)
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if flag.NArg() > 1 {
		return fmt.Errorf("usage: update-demo [--builds DIR] [--instance NAME] [daemon|stop|status]")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	options := daemon.Options{AppID: "daemonkit-demo", InstanceID: *instance, Version: version}
	if flag.Arg(0) == "daemon" {
		return runDaemon(ctx, options)
	}
	if flag.Arg(0) != "" && flag.Arg(0) != "stop" && flag.Arg(0) != "status" {
		return fmt.Errorf("unknown command %q", flag.Arg(0))
	}
	manager, err := daemon.NewManager(options, "--instance", *instance, "daemon")
	if err != nil {
		return err
	}
	if flag.Arg(0) == "status" {
		status, statusErr := manager.Status(ctx)
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return errors.Join(statusErr, encoder.Encode(status))
	}
	if flag.Arg(0) == "stop" {
		return manager.Stop(ctx)
	}
	builds, err := discoverBuilds(*buildDir)
	if err != nil {
		return err
	}
	session := &session{manager: manager, options: options}
	// Cancel in-flight commands before closing the shared client.
	defer func() { cancel(); session.close() }()
	_, err = tea.NewProgram(newModel(ctx, session, builds), tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}
