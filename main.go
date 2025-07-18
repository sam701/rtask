package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/lmittmann/tint"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"
)

func main() {
	setupLogging()

	app := &cli.Command{
		Name:  "t8sk",
		Usage: "task runner with API key management",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config",
				Value: "./config.toml",
				Usage: "Path to the TOML file containing configuration",
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "run",
				Usage: "Run the task server",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "api-address",
						Value: "localhost:8800",
						Usage: "Host address to listen on",
					},
					&cli.StringFlag{
						Name:  "metrics-address",
						Value: "localhost:9090",
						Usage: "Metrics address to listen on",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runServer(cmd.String("config"), cmd.String("api-address"), cmd.String("metrics-address"))
				},
			},
			{
				Name:  "add-key",
				Usage: "Add a new API key",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args()
					if args.Len() < 1 {
						return fmt.Errorf("name argument is required")
					}
					name := args.Get(0)

					config, err := readConfig(cmd.String("config"))
					if err != nil {
						return err
					}

					return addKey(name, config.ApiKeysFile)
				},
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		slog.Error("Error running application", "error", err)
	}
}

func runMetricsServer(metricsAddress string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	slog.Info("starting metrics server", "address", metricsAddress, "path", "/metrics")
	if err := http.ListenAndServe(metricsAddress, mux); err != nil {
		slog.Error("Error running metrics server", "error", err)
	}
}

func runServer(configFile, listenAddress, metricsAddress string) error {
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}

	var apiKeys map[string]string
	if !filepath.IsAbs(config.ApiKeysFile) {
		// Make API keys file path relative to config file directory
		configDir := filepath.Dir(configFile)
		config.ApiKeysFile = filepath.Join(configDir, config.ApiKeysFile)
	}
	_, err = toml.DecodeFile(config.ApiKeysFile, &apiKeys)
	if err != nil {
		return fmt.Errorf("failed decoding api keys file: %w", err)
	}

	for name, task := range config.Tasks {
		handler, err := NewTaskHandler(name, &task, apiKeys)
		if err != nil {
			return fmt.Errorf("failed creating task handler for %s: %w", name, err)
		}
		http.Handle("/tasks/"+name, handler)
	}

	go runMetricsServer(metricsAddress)

	slog.Info("starting server", "address", listenAddress)
	if err := http.ListenAndServe(listenAddress, nil); err != nil {
		return fmt.Errorf("failed starting server: %w", err)
	}
	return nil
}

func setupLogging() {
	val, valSet := os.LookupEnv("GO_LOG")
	logLevel := slog.LevelInfo
	var err error
	if valSet {
		err = logLevel.UnmarshalText([]byte(val))
	}
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{
		Level:      logLevel,
		TimeFormat: time.RFC3339,
	})))
	if err != nil {
		slog.Warn("invalid GO_LOG value, ", "GO_LOG", val, "error", err)
	}
}
