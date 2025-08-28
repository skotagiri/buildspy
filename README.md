# BuildSpy

A real-time build system monitoring tool that tracks process spawning, resource utilization, and provides flame graph visualization for build systems like Make, Ninja, CMake, and others.

## Features

- **Process Tree Monitoring**: Track all child processes spawned by build systems
- **Real-time Flame Graph**: Interactive visualization of build process hierarchy
- **Resource Tracking**: CPU and memory utilization per process
- **Thread Monitoring**: Detailed thread-level information for each process
- **Build System Detection**: Automatic detection of Make, Ninja, CMake, Bazel, and more
- **Performance Analysis**: Identify bottlenecks and optimization opportunities
- **Web Dashboard**: Real-time monitoring via web interface

## Installation

```bash
git clone <repository-url>
cd buildspy
go build -o buildspy
```

## Usage

### Basic Usage
```bash
# Monitor a make build
./buildspy -cmd make

# Monitor with specific arguments
./buildspy -cmd make clean all

# Monitor ninja build
./buildspy -cmd ninja

# Enable verbose logging
./buildspy -cmd make -v

# Custom web server port
./buildspy -cmd make -port 9090
```

### Web Dashboard

Once running, open http://localhost:8080 to view:
- Real-time flame graph of process hierarchy  
- Process timeline with start/end events
- Resource utilization table with CPU/memory stats
- Build system analysis and optimization suggestions

## Examples

### Monitoring a Make Build
```bash
./buildspy -cmd make -v
```
Output:
```
Started build process: make (PID: 12345)
Web server starting on http://localhost:8080
New process detected: PID 12346
Process ended: PID 12346 (gcc)
```

### Monitoring Ninja Build
```bash
./buildspy -cmd ninja -port 9090
```

### Complex Build Command
```bash
./buildspy -cmd cmake --build . --parallel 4
```

## Architecture

### Core Components
- **BuildMonitor**: Main orchestrator tracking process trees
- **ProcessInfo**: Detailed process metadata and resource usage
- **ThreadMonitor**: Thread-level monitoring via /proc filesystem
- **BuildSystem Detection**: Automatic build system identification
- **WebSocket Server**: Real-time data streaming to dashboard

### Data Collection
- Process spawning/termination events via process tree scanning
- CPU/memory usage via gopsutil library
- Thread information from /proc/[pid]/task/[tid]/ 
- Build system analysis from project files

## API Endpoints

- `GET /`: Web dashboard
- `GET /ws`: WebSocket for real-time updates  
- `GET /api/processes`: Current process information
- `GET /api/events`: Build event timeline

## Performance

- High-frequency monitoring (100ms intervals)
- Efficient process tree traversal
- Non-blocking WebSocket broadcasting
- Minimal overhead on build performance

## Supported Build Systems

- **Make**: Makefile, makefile, GNUmakefile
- **Ninja**: build.ninja
- **CMake**: CMakeLists.txt  
- **Bazel**: BUILD, BUILD.bazel, WORKSPACE
- **Gradle**: build.gradle, build.gradle.kts
- **Cargo**: Cargo.toml
- **npm**: package.json

## Requirements

- Go 1.21+
- Linux (uses /proc filesystem for thread monitoring)
- Build system (make, ninja, etc.)