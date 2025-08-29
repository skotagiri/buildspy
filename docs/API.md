# BuildSpy Daemon API Reference

This document describes the HTTP API endpoints provided by the BuildSpy daemon (`buildspyd`).

## Base Configuration

- **Default Port**: 8080
- **Base URL**: `http://localhost:8080`
- **Content-Type**: `application/json` for all API endpoints

## Data Models

### BuildRun
```go
type BuildRun struct {
    ID           string     `json:"id"`          // UUID
    Command      string     `json:"command"`     // Build command
    Args         []string   `json:"args"`        // Command arguments
    StartTime    time.Time  `json:"start_time"`  // Build start time
    EndTime      *time.Time `json:"end_time,omitempty"` // Build end time (null if running)
    Status       string     `json:"status"`      // "running", "completed", "failed", "running_live"
    ExitCode     *int       `json:"exit_code,omitempty"` // Exit code (-1 for crashed builds)
    WorkingDir   string     `json:"working_dir"` // Execution directory
    ProcessCount int        `json:"process_count"` // Total processes spawned
    Duration     *int64     `json:"duration,omitempty"` // Duration in milliseconds
}
```

### ProcessInfo
```go
type ProcessInfo struct {
    BuildRunID string     `json:"build_run_id"` // Associated build run
    PID        int32      `json:"pid"`
    PPID       int32      `json:"ppid"`
    Name       string     `json:"name"`
    Cmdline    string     `json:"cmdline"`
    StartTime  time.Time  `json:"start_time"`
    EndTime    *time.Time `json:"end_time,omitempty"`
    CPUUsage   float64    `json:"cpu_usage"`
    MemUsage   uint64     `json:"mem_usage"`
    Status     string     `json:"status"`
    Children   []int32    `json:"children"`
}
```

### BuildEvent
```go
type BuildEvent struct {
    ID         string      `json:"id"`         // UUID
    BuildRunID string      `json:"build_run_id"` // Associated build run
    Timestamp  time.Time   `json:"timestamp"`
    Type       string      `json:"type"` // "process_start", "process_end", "resource_update", "build_complete", "build_crashed"
    Process    ProcessInfo `json:"process"`
}
```

## HTTP API Endpoints

### GET /
Serves the web dashboard interface (HTML page).

### GET /api/runs
Lists completed build runs (excludes live builds).

**Query Parameters:**
- `search` (optional): Search builds by command or args
- `limit` (optional): Limit number of results (default: 50)

**Response:** Array of `BuildRun` objects

### GET /api/live
Lists currently running build runs.

**Response:** Array of `BuildRun` objects with `status: "running_live"`

### GET /api/runs/{id}
Get details for a specific build run.

**Path Parameters:**
- `id`: Build run UUID

**Response:** `BuildRun` object (includes `status: "running_live"` if currently running)

### GET /api/runs/{id}/events
Get all events for a specific build run.

**Path Parameters:**
- `id`: Build run UUID

**Response:** Array of `BuildEvent` objects

### GET /api/runs/{id}/processes
Get all processes for a specific build run.

**Path Parameters:**
- `id`: Build run UUID

**Response:** Array of `ProcessInfo` objects

### GET /api/stats
Get daemon statistics.

**Response:**
```json
{
    "total_runs": 150,
    "live_builds": 2,
    "by_status": {
        "completed": 120,
        "failed": 28,
        "running": 2
    }
}
```

### POST /api/live/submit
Submit live build data (used by CLI tools).

**Request Body:**
```json
{
    "type": "build_run|build_event|process",
    "data": <BuildRun|BuildEvent|ProcessInfo object>
}
```

**Response:**
```json
{
    "status": "success"
}
```

### DELETE /api/runs
Delete multiple build runs (bulk delete).

**Query Parameters:**
- `run_id` (required, multiple): Build run UUIDs to delete

**Response:**
```json
{
    "deleted_count": 5,
    "total_requested": 6,
    "errors": ["Cannot delete live build: abc123"]
}
```

### DELETE /api/runs/{id}
Delete a specific build run.

**Path Parameters:**
- `id`: Build run UUID

**Response:**
```json
{
    "status": "deleted"
}
```

### POST /api/delete
Delete multiple build runs (bulk delete with JSON body).

**Request Body:**
```json
{
    "run_ids": ["uuid1", "uuid2", "uuid3"]
}
```

**Response:**
```json
{
    "deleted_count": 2,
    "total_requested": 3,
    "errors": ["Cannot delete live build: uuid3"]
}
```

## WebSocket Endpoints

### WebSocket /ws/live
Real-time updates for all live builds.

**Messages Sent:**
- Initial state with recent builds
- Real-time build events as they occur

### WebSocket /ws/runs/{id}
Real-time updates for a specific build run.

**Path Parameters:**
- `id`: Build run UUID

**Messages Sent:**
- Historical events for the run on connect
- Real-time events as they occur during the build

## Status Values

### BuildRun Status
- `"running"` - Build completed but not live-monitored
- `"running_live"` - Currently running with live monitoring
- `"completed"` - Build finished successfully (exit code 0)
- `"failed"` - Build failed (non-zero exit code or crashed)

### Process Status
- `"running"` - Process is active
- `"completed"` - Process finished
- `"crashed"` - Process terminated unexpectedly

### Event Types
- `"process_start"` - New process started
- `"process_end"` - Process terminated
- `"resource_update"` - CPU/memory usage update
- `"build_complete"` - Build finished normally
- `"build_crashed"` - Build detected as crashed (timeout)

## Error Responses

All error responses return appropriate HTTP status codes with error messages:
- `400 Bad Request` - Invalid request data
- `404 Not Found` - Resource not found
- `405 Method Not Allowed` - HTTP method not supported
- `500 Internal Server Error` - Server error

## Background Processing

The daemon runs background tasks every 10 seconds to:
- Monitor live builds for timeout/crashes (30-second timeout)
- Automatically mark stale builds as failed with exit code -1
- Generate crash events for timed-out builds
- Clean up disconnected WebSocket subscribers