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

	// Map of user friendly names (appearing in metrics and logs) to webhook hashes.
	WebhookSecrets map[string]string

	// Map of user friendly names (appearing in metrics and logs) to paths containing webhook hash.
	WebhookSecretFiles map[string]string

	// Environment variables
	Environment map[string]string

	// The specified request headers will be passed to the process as REQUEST_HEADER_<name> envvar,
	// where the <name> is in uppercase and all disallowed characters are replaced by underscores.
	PassRequestHeaders []string

	// If true, return task ID immediately and run in background.
	// If false, wait for completion and return result.
	Async bool

	MaxInputBytes  int64
	MaxOutputBytes int64

	// If the task exceeds the provided execution timeout, it will be killed and the exit_code will be set to -1.
	ExecutionTimeoutSeconds int64

	// If true, merge stderr into stdout. Default false (keep separate)
	MergeStderr bool

	// Histogram buckets for task duration metrics in seconds
	DurationHistogramBuckets []float64

	// Requests per second. Default 0 = unlimited
	RateLimit float64

	// Maximum number of concurrent task executions. Default 0 = unlimited
	MaxConcurrentTasks int
}

// applyDefaults sets default values for unspecified configuration options
func (t *Task) applyDefaults() {
	if t.MaxInputBytes <= 0 {
		t.MaxInputBytes = 16 * 1024
	}

	if t.MaxOutputBytes <= 0 {
		t.MaxOutputBytes = 16 * 1024
	}

	if t.ExecutionTimeoutSeconds <= 0 {
		t.ExecutionTimeoutSeconds = 30
	}
}

func readConfig(configFile string) (*Config, error) {
	var config Config
	_, err := toml.DecodeFile(configFile, &config)
	if err != nil {
		return nil, fmt.Errorf("error decoding config file: %w", err)
	}

	// Set defaults for all tasks
	for name := range config.Tasks {
		task := config.Tasks[name]
		task.applyDefaults()
		config.Tasks[name] = task
	}

	return &config, nil
}
