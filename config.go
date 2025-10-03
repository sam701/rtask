package main

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Maps task names to the task definitions.
	Tasks map[string]Task
}

type Task struct {
	// Command with its arguments
	Command     []string
	Workdir     string
	APIKeyNames []string

	// Map of user friendly names (appearing in metrics and logs) to paths containing webhook hash.
	WebhookSecretFiles map[string]string

	// Environment variables
	Environment map[string]string

	// If true, the handler will pipe stdout and stderr to the response body
	Blocking bool

	MaxInputBytes  int64
	MaxOutputBytes int64

	ExecutionTimeoutSeconds int64

	// If true, stderr is redirected to stdout. Default true
	RedirectStderr *bool

	// Histogram buckets for task duration metrics in seconds
	DurationHistogramBuckets []float64

	// Requests per second. Default 0 = unlimited
	RateLimit float64

	// Maximum number of concurrent task executions. Default 0 = unlimited
	MaxConcurrentTasks int
}

func readConfig(configFile string) (*Config, error) {
	var config Config
	_, err := toml.DecodeFile(configFile, &config)
	if err != nil {
		return nil, fmt.Errorf("error decoding config file: %w", err)
	}

	return &config, nil
}
