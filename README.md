# RTask

RTask is a secure task runner with API key management that exposes system commands as HTTP endpoints. It provides authenticated access to configured tasks with rate limiting, metrics collection, and secure API key management.

## Features

- **Secure API Key Management**: Argon2-based key hashing with CBOR encoding
- **Rate Limiting**: Configurable per-task rate limiting to prevent abuse
- **Metrics Collection**: Prometheus metrics for monitoring task execution and rejections
- **Task Configuration**: TOML-based configuration for defining tasks and their permissions
- **Concurrent Safety**: Mutex-based locking to prevent concurrent task execution
- **Flexible Execution**: Support for both blocking and non-blocking task execution

## Installation

### Prerequisites

- Go 1.24.3 or later
- Optional: [just](https://github.com/casey/just) for build automation

### Build from Source

```bash
git clone <repository-url>
cd rtask.go
go build
```

Or using just:

```bash
just build
```

## Configuration

RTask uses TOML configuration files to define tasks and API key settings.

### Main Configuration (`config.toml`)

```toml
apiKeysFile = "./api-keys.toml"

[tasks.hello]
command = ["./test.nu"]
apiKeyNames = ["a1", "a3"]
blocking = true
rateLimit = 1.0

[tasks.build]
command = ["make", "build"]
workdir = "/path/to/project"
apiKeyNames = ["github-action"]
blocking = false
rateLimit = 0.5
```

### Task Configuration Options

- `command`: Array of command and arguments to execute
- `workdir`: Working directory for command execution (optional)
- `apiKeyNames`: List of API key names that can access this task
- `blocking`: If true, streams stdout/stderr to HTTP response; if false, runs asynchronously
- `rateLimit`: Requests per second (default: 0.5)

## Usage

### Starting the Server

```bash
# Start with default configuration
./rtask run

# Start with custom configuration and addresses
./rtask run --config ./my-config.toml --api-address localhost:8080 --metrics-address localhost:9091
```

### Managing API Keys

```bash
# Add a new API key
./rtask add-key my-key-name
```

This will output an API key that can be used to authenticate requests.

### Making Requests

Send HTTP requests to task endpoints with Bearer authentication:

```bash
# Execute a task
curl -X POST \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d "input data" \
  http://localhost:8800/tasks/hello
```

### Monitoring

Prometheus metrics are available at the metrics endpoint:

```bash
curl http://localhost:9090/metrics
```

Available metrics:
- `task_duration_seconds`: Histogram of task execution times
- `task_rejection_total`: Counter of rejected requests by reason

## API Endpoints

- `POST /tasks/{task-name}`: Execute the specified task
- `GET /metrics`: Prometheus metrics endpoint

## Security Features

- **API Key Hashing**: Keys are hashed using Argon2 with configurable parameters
- **Bearer Token Authentication**: Standard HTTP Bearer token authentication
- **Rate Limiting**: Per-task rate limiting to prevent abuse
- **Concurrent Execution Control**: Mutex locking prevents multiple simultaneous executions
- **Secure Key Storage**: API keys are stored in TOML files with restricted permissions (0600)

## Error Responses

- `401 Unauthorized`: Missing or invalid API key
- `429 Too Many Requests`: Rate limit exceeded or task already running
- `500 Internal Server Error`: Task execution failed

## Development

### Running in Debug Mode

```bash
GO_LOG=debug ./rtask run
```

### Using Just

```bash
# Build the project
just build

# Run with debug logging
just run run --config config.toml
```

## License

[MIT](./LICENSE)
