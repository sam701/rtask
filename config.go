package main

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Maps task names to the task definitions.
	Tasks map[string]Task

	// Path to the TOML file containing API keys.
	APIKeysFile string
}

type Task struct {
	// Command with its arguments
	Command     []string
	Workdir     string
	APIKeyNames []string

	// If true, the handler will pipe stdout and stderr to the response body
	Blocking bool

	// Requests per second. Default 0.5
	RateLimit float64
}

func readConfig(configFile string) (*Config, error) {
	var config Config
	_, err := toml.DecodeFile(configFile, &config)
	if err != nil {
		return nil, fmt.Errorf("error decoding config file: %w", err)
	}

	if config.APIKeysFile == "" {
		return nil, fmt.Errorf("missing required field api_keys_file in config")
	}

	return &config, nil
}
