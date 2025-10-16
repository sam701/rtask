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
[tasks.hello]
command = ["./test.nu"]
apiKeyNames = ["a1", "a3"]
async = false
rateLimit = 1.0

[tasks.build]
command = ["make", "build"]
workdir = "/path/to/project"
apiKeyNames = ["github-action"]
async = true
rateLimit = 0.5
maxConcurrentTasks = 2
executionTimeoutSeconds = 300

[tasks.webhook-handler]
command = ["/usr/local/bin/process-webhook.sh"]
async = true
passRequestHeaders = ["X-GitHub-Event", "X-Hub-Signature"]

[tasks.webhook-handler.webhookSecrets]
github = "your-webhook-secret-hash"

[tasks.webhook-handler.webhookSecretFiles]
gitlab = "/etc/rtask/gitlab-webhook-secret"

[tasks.webhook-handler.environment]
LOG_LEVEL = "info"
WEBHOOK_TIMEOUT = "30"
```

### Task Configuration Options

- `command`: Array of command and arguments to execute
- `workdir`: Working directory for command execution (optional)
- `apiKeyNames`: List of API key names that can access this task via `/tasks/{task-name}` endpoint
- `webhookSecrets`: Map of user-friendly names to webhook secret hashes for webhook authentication
- `webhookSecretFiles`: Map of user-friendly names to file paths containing webhook secret hashes
- `environment`: Map of environment variables to pass to the task
- `passRequestHeaders`: List of HTTP request headers to pass as environment variables (e.g., `X-Custom-Header` becomes `REQUEST_HEADER_X_CUSTOM_HEADER`)
- `async`: If true, returns task ID immediately and runs in background; if false, waits for completion and returns result
- `rateLimit`: Requests per second (default: 0 = unlimited)
- `maxConcurrentTasks`: Maximum number of concurrent task executions (default: 0 = unlimited)
- `maxInputBytes`: Maximum input size in bytes (default: 16KB)
- `maxOutputBytes`: Maximum output size in bytes (default: 16KB)
- `executionTimeoutSeconds`: Task execution timeout in seconds (default: 30)
- `mergeStderr`: If true, merge stderr into stdout (default: false, keep separate)
- `durationHistogramBuckets`: Custom histogram buckets for task duration metrics

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

#### Synchronous Tasks

For tasks configured with `async = false`, the response contains the execution result:

```bash
curl -X POST \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d "input data" \
  http://localhost:8800/tasks/hello

# Response:
# {
#   "status": "success",
#   "exit_code": 0,
#   "stdout": "task output",
#   "stderr": ""
# }
```

#### Async Tasks

For tasks configured with `async = true`, you receive a task ID immediately:

```bash
# Submit task
curl -X POST \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d "input data" \
  http://localhost:8800/tasks/build

# Response: {"task_id": "kj4w2ndvmfzxg2lk"}

# Poll for result
curl -H "Authorization: Bearer YOUR_API_KEY" \
  http://localhost:8800/tasks/build/kj4w2ndvmfzxg2lk

# Response when complete:
# {
#   "status": "success",
#   "exit_code": 0,
#   "stdout": "build output",
#   "stderr": ""
# }
```

#### Webhooks

For tasks configured with webhooks, no API key is required:

```bash
curl -X POST \
  -d "webhook payload" \
  http://localhost:8800/wh/your-webhook-hash
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

### Task Execution

- `POST /tasks/{task-name}`: Execute the specified task with API key authentication
  - **Synchronous mode** (`async = false`): Returns task execution result immediately
  - **Async mode** (`async = true`): Returns `{"task_id": "..."}` immediately
- `GET /tasks/{task-name}/{task-id}`: Retrieve async task result by task ID
  - Returns `404` if task not found (never existed or cleaned up after retention period)
  - Task results are retained for a configurable period after completion (default: 20 seconds)

### Webhooks

- `POST /wh/{webhook-hash}`: Execute task via webhook (no API key required)
  - Webhook hash is derived from `webhookSecrets` or `webhookSecretFiles` configuration
  - Key name is automatically set in the context for metrics

### Monitoring

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
