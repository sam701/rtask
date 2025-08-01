package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lmittmann/tint"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"
)

func main() {
	setupLogging()

	app := &cli.Command{
		Name:  "rtask",
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
				Arguments: []cli.Argument{
					&cli.StringArg{
						Name: "API_KEY_NAME",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					config, err := readConfig(cmd.String("config"))
					if err != nil {
						return err
					}

					return addKey(cmd.StringArg("API_KEY_NAME"), config.APIKeysFile)
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

	keyStore, err := NewStore(config.APIKeysFile)
	if err != nil {
		return fmt.Errorf("failed to create key store: %w", err)
	}
	r := chi.NewRouter()
	for name, task := range config.Tasks {
		tm, err := NewTaskManager(name, &task, keyStore)
		if err != nil {
			return fmt.Errorf("failed creating task manager for %s: %w", name, err)
		}
		tm.ConfigureRoutes(r)
	}

	go runMetricsServer(metricsAddress)

	slog.Info("starting server", "address", listenAddress)
	if err := http.ListenAndServe(listenAddress, r); err != nil {
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
