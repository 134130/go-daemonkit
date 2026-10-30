package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/config"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

type demoConfig struct {
	Daemon struct {
		Greeting string `toml:"greeting"`
	} `toml:"daemon"`
}

func configFile(options daemon.Options, path string) (*config.File[demoConfig], error) {
	return config.New(config.Options[demoConfig]{AppID: options.AppID, Path: path,
		Defaults: func() demoConfig { var c demoConfig; c.Daemon.Greeting = "Hello"; return c },
		Validate: func(_ context.Context, c demoConfig) error {
			if strings.TrimSpace(c.Daemon.Greeting) == "" || len(c.Daemon.Greeting) > 80 {
				return errors.New("daemon.greeting must contain 1–80 bytes")
			}
			return nil
		},
	})
}

func runConfig(ctx context.Context, options daemon.Options, path string, args []string) error {
	file, err := configFile(options, path)
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "reload" {
		client, err := daemon.Connect(ctx, options)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		result, err := client.Invoke(ctx, api.ReloadConfig, api.ReloadRequest{Path: file.Path()})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "Applied revision %d\n", result.Revision)
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "path":
		_, err = fmt.Fprintln(os.Stdout, file.Path())
	case len(args) == 2 && args[0] == "get":
		var value any
		value, err = file.Get(ctx, args[1])
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(value)
		}
	case len(args) == 3 && args[0] == "set":
		err = file.Set(ctx, args[1], args[2])
	case len(args) == 1 && args[0] == "edit":
		err = file.Edit(ctx)
	default:
		return errors.New("usage: config path|get KEY|set KEY TOML_VALUE|edit|reload")
	}
	if err == nil && (args[0] == "set" || args[0] == "edit") {
		_, err = fmt.Fprintln(os.Stdout, "File saved; use config reload to apply it to the running daemon.")
	}
	return err
}
