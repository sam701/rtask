package main

type Config struct {
	// Maps task names to the task definitions.
	Tasks map[string]Task

	// Path to the TOML file containing API keys.
	ApiKeysFile string
}

type Task struct {
	// Command with its arguments
	Command     []string
	Workdir     string
	ApiKeyNames []string

	// If true, the handler will pipe stdout and stderr to the response body
	Blocking bool

	// Requests per second. Default 0.5
	RateLimit float64
}
