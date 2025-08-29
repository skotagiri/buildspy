# BuildSpy

A comprehensive build system monitoring toolchain that captures, stores, and visualizes build process metrics. BuildSpy provides both real-time monitoring and historical analysis of build performance through a two-component architecture.

## Architecture

BuildSpy consists of two main components:

### 🔧 buildspy (CLI Tool)
- **Purpose**: Captures build metrics and stores them in a local database
- **Focus**: Lightweight data collection with minimal overhead
- **Storage**: Persistent BadgerDB database for long-term retention

### 🖥️ buildspyd (Daemon Service)  
- **Purpose**: Serves historical build data via web interface and API
- **Features**: Multi-run management, search, real-time updates
- **Interface**: Modern web dashboard with interactive visualizations

## Features

- **Process Tree Monitoring**: Track all child processes spawned by build systems
- **Persistent Storage**: All build data stored permanently with BadgerDB
- **Multi-Run Management**: View and compare multiple build executions
- **Real-time + Historical**: Monitor live builds or analyze past runs
- **Resource Tracking**: CPU and memory utilization per process
- **Interactive Dashboard**: Rich web interface with timeline visualizations
- **Search & Filter**: Find specific builds by command, date, or status
- **Performance Analysis**: Identify bottlenecks and optimization opportunities

## Installation

```bash
git clone <repository-url>
cd buildspy
make all
```

This creates two binaries:
- `buildspy`: CLI monitoring tool
- `buildspyd`: Web dashboard daemon

## Quick Start

### 1. Start the Dashboard Daemon
```bash
./buildspyd
# Dashboard available at http://localhost:8080
```

### 2. Monitor Your First Build
```bash
# In another terminal
./buildspy -cmd "make -j4"
# Build data automatically saved to database
```

### 3. View Results
Open http://localhost:8080 to see:
- List of all your build runs
- Click any run to view its timeline and process details
- Search and filter your build history

## Usage

### buildspy (CLI Tool)

**Monitor any build command:**
```bash
# Basic usage
./buildspy -cmd "make"
./buildspy -cmd "ninja -v"  
./buildspy -cmd "cmake --build . --parallel 8"

# With options
./buildspy -cmd "make test" -v                    # Verbose output
./buildspy -cmd "make" -data /path/to/data       # Custom data directory
```

**The CLI tool will:**
- Execute your build command normally (you see all output)
- Monitor all spawned processes in background
- Save complete build metrics to database
- Exit when build completes

### buildspyd (Daemon Service)

**Start the web dashboard:**
```bash
# Default (port 8080)
./buildspyd

# Custom options
./buildspyd -port 9090                           # Custom port
./buildspyd -data /path/to/data                  # Custom data directory  
./buildspyd -v                                   # Verbose logging
```

**Web Interface Features:**
- **📋 Build Runs List**: All your builds with status, duration, process count
- **🔍 Search**: Filter builds by command, arguments, or working directory
- **📊 Build Details**: Click any run to see detailed timeline visualization
- **🔴 Live Mode**: Monitor currently running builds in real-time
- **📈 Statistics**: Overview of total runs, completion rates, etc.

## Examples

### Complete Workflow Example

```bash
# 1. Start the dashboard
./buildspyd &

# 2. Monitor several builds
./buildspy -cmd "make clean"
./buildspy -cmd "make -j16" 
./buildspy -cmd "make test"
./buildspy -cmd "make install"

# 3. View results at http://localhost:8080
# - See all 4 builds in the runs list
# - Compare their performance
# - Identify which processes took longest
```

### Continuous Integration Usage

```bash
# In your CI script
./buildspyd -port 8080 &  # Start daemon
DAEMON_PID=$!

# Monitor your build
./buildspy -cmd "make -j$(nproc) all test"

# Dashboard remains available for analysis
# Kill daemon when done: kill $DAEMON_PID
```

### Development Workflow

```bash
# Keep daemon running during development
./buildspyd &

# Monitor builds throughout the day
./buildspy -cmd "make debug"
./buildspy -cmd "make test"
./buildspy -cmd "make release"

# View historical trends and performance improvements
```

## API Reference

buildspyd provides a REST API for programmatic access:

```bash
# List all build runs
curl http://localhost:8080/api/runs

# Get specific build details
curl http://localhost:8080/api/runs/{id}
curl http://localhost:8080/api/runs/{id}/events
curl http://localhost:8080/api/runs/{id}/processes

# Get statistics
curl http://localhost:8080/api/stats

# Search builds
curl http://localhost:8080/api/runs?search=make
curl http://localhost:8080/api/runs?limit=10
```

WebSocket endpoints for real-time updates:
- `ws://localhost:8080/ws/live` - Live build monitoring
- `ws://localhost:8080/ws/runs/{id}` - Specific run updates

## Data Storage

- **Location**: `~/.buildspy/` (configurable with `-data` flag)
- **Database**: BadgerDB (embedded key-value store)
- **Retention**: Data persists indefinitely (no automatic cleanup yet)
- **Format**: JSON-serialized build runs, events, and process information

### Data Directory Structure
```
~/.buildspy/
├── buildspy.db/          # BadgerDB files
│   ├── 000001.vlog       # Value log
│   ├── 000002.sst        # Sorted string tables  
│   └── MANIFEST         # Database manifest
```

## Build System Support

BuildSpy works with any command-line build system:

- **Make**: `make`, `gmake`
- **Ninja**: `ninja`  
- **CMake**: `cmake --build`
- **Bazel**: `bazel build`
- **Cargo**: `cargo build`
- **npm/yarn**: `npm run build`
- **Maven**: `mvn compile`
- **Gradle**: `gradle build`
- **Custom scripts**: Any executable command

## Requirements

- **Go 1.21+** (for building from source)
- **Linux/macOS** (uses process monitoring APIs)
- **~10MB disk space** per build (varies by build complexity)
- **Minimal CPU overhead** (~1-2% during builds)

## Performance

- **Monitoring frequency**: 50ms intervals for responsive tracking
- **Database performance**: Optimized for write-heavy workloads
- **Memory usage**: Minimal footprint, data streamed to disk
- **Build overhead**: Typically <1% impact on build time

## Troubleshooting

### Common Issues

**buildspy exits immediately:**
```bash
# Check if command is valid
./buildspy -cmd "nonexistent-command"  # Will fail
./buildspy -cmd "make" -v              # Use -v for debugging
```

**buildspyd can't access database:**
```bash
# Check permissions on data directory
ls -la ~/.buildspy/
./buildspyd -data ./custom-data        # Use custom location
```

**Web dashboard shows no builds:**
```bash
# Verify buildspy saved data successfully
./buildspy -cmd "echo hello" -v        # Test with simple command
```

### Development

```bash
# Build from source
make all

# Run in development mode
make dev-buildspy ARGS='-cmd "make test"'
make dev-buildspyd ARGS='-port 9090'

# Clean builds
make clean
```