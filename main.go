package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/lmittmann/tint"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	configFile     = flag.String("config", "./config.toml", "Path to the TOML file containing configuration")
	listenAddress  = flag.String("api-address", "localhost:8800", "Host address to listen on")
	metricsAddress = flag.String("metrics-address", "localhost:9090", "Metrics address to listen on")
)

func main() {
	setupLogging()

	if err := run(); err != nil {
		slog.Error("Error running application", "error", err)
	}
}

func runMetricsServer() {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	slog.Info("starting metrics server", "address", *metricsAddress, "path", "/metrics")
	if err := http.ListenAndServe(*metricsAddress, mux); err != nil {
		slog.Error("Error running metrics server", "error", err)
	}
}

func run() error {
	var config Config
	_, err := toml.DecodeFile(*configFile, &config)
	if err != nil {
		return fmt.Errorf("error decoding config file: %w", err)
	}

	if config.ApiKeysFile == "" {
		return fmt.Errorf("missing required field api_keys_file in config")
	}

	var apiKeys map[string]string
	if !filepath.IsAbs(config.ApiKeysFile) {
		// Make API keys file path relative to config file directory
		configDir := filepath.Dir(*configFile)
		config.ApiKeysFile = filepath.Join(configDir, config.ApiKeysFile)
	}
	_, err = toml.DecodeFile(config.ApiKeysFile, &apiKeys)
	if err != nil {
		return fmt.Errorf("failed decoding api keys file: %w", err)
	}

	for name, task := range config.Tasks {
		http.Handle("/tasks/"+name, NewTaskHandler(name, &task, apiKeys))
	}

	go runMetricsServer()

	slog.Info("starting server", "address", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
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
