package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/process"
)

// ProcessInfo contains detailed information about a monitored process
type ProcessInfo struct {
	PID       int32     `json:"pid"`
	PPID      int32     `json:"ppid"`
	Name      string    `json:"name"`
	Cmdline   string    `json:"cmdline"`
	StartTime time.Time `json:"start_time"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	CPUUsage  float64   `json:"cpu_usage"`
	MemUsage  uint64    `json:"mem_usage"`
	Status    string    `json:"status"`
	Children  []int32   `json:"children"`
}

// BuildEvent represents a timeline event during the build
type BuildEvent struct {
	Timestamp time.Time   `json:"timestamp"`
	Type      string      `json:"type"` // "process_start", "process_end", "resource_update"
	Process   ProcessInfo `json:"process"`
}

// BuildMonitor tracks build processes and their resource usage
type BuildMonitor struct {
	processes   map[int32]*ProcessInfo // PID -> ProcessInfo
	events      []BuildEvent           // Timeline of all events
	mutex       sync.RWMutex
	verbose     bool
	ctx         context.Context
	cancel      context.CancelFunc
	subscribers []chan BuildEvent // WebSocket subscribers
	buildDone   bool              // Track if build has completed
}

// NewBuildMonitor creates a new build monitoring instance
func NewBuildMonitor(verbose bool) *BuildMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &BuildMonitor{
		processes:   make(map[int32]*ProcessInfo),
		events:      make([]BuildEvent, 0),
		verbose:     verbose,
		ctx:         ctx,
		cancel:      cancel,
		subscribers: make([]chan BuildEvent, 0),
	}
}

// MonitorBuild starts monitoring a build command and all its child processes
func (bm *BuildMonitor) MonitorBuild(command string, args ...string) error {
	// Start the build process
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group to track all children
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start build command: %w", err)
	}

	rootPID := int32(cmd.Process.Pid)
	if bm.verbose {
		fmt.Printf("Started build process: %s (PID: %d)\n", command, rootPID)
	}

	// Start monitoring goroutine
	go bm.monitorProcessTree(rootPID)

	// Wait for the build to complete in a separate goroutine
	go func() {
		err := cmd.Wait()
		bm.mutex.Lock()
		bm.buildDone = true
		bm.mutex.Unlock()
		
		if bm.verbose {
			if err != nil {
				fmt.Printf("Build completed with error: %v\n", err)
			} else {
				fmt.Printf("Build completed successfully\n")
			}
			fmt.Printf("Keeping server running for visualization... (press Ctrl+C to exit)\n")
		}
		
		// Send build completion event
		event := BuildEvent{
			Timestamp: time.Now(),
			Type:      "build_complete",
			Process: ProcessInfo{
				PID:    rootPID,
				Name:   "build",
				Status: "completed",
			},
		}
		bm.events = append(bm.events, event)
		bm.broadcastEvent(event)
	}()

	return nil
}

// monitorProcessTree continuously monitors the process tree starting from rootPID
func (bm *BuildMonitor) monitorProcessTree(rootPID int32) {
	ticker := time.NewTicker(100 * time.Millisecond) // High frequency monitoring
	defer ticker.Stop()

	for {
		select {
		case <-bm.ctx.Done():
			return
		case <-ticker.C:
			// Check if we should continue monitoring
			bm.mutex.RLock()
			buildDone := bm.buildDone
			hasActiveProcesses := len(bm.processes) > 0
			bm.mutex.RUnlock()
			
			// Continue monitoring if build is running or if there are still active processes to track
			if !buildDone || hasActiveProcesses {
				bm.updateProcessTree(rootPID)
			}
		}
	}
}

// updateProcessTree scans and updates information about all processes in the tree
func (bm *BuildMonitor) updateProcessTree(rootPID int32) {
	// Get all processes to find children
	allProcs, err := process.Processes()
	if err != nil {
		return
	}

	bm.mutex.Lock()
	defer bm.mutex.Unlock()

	// Track active PIDs in this scan
	activePIDs := make(map[int32]bool)

	// Find all processes that are part of our build tree
	for _, proc := range allProcs {
		pid := proc.Pid
		
		// Check if this process is our root or a descendant
		if pid == rootPID || bm.isDescendant(pid, rootPID) {
			activePIDs[pid] = true
			
			// Get or create process info
			if _, exists := bm.processes[pid]; !exists {
				bm.processes[pid] = &ProcessInfo{
					PID:       pid,
					StartTime: time.Now(),
				}
				
				// Log new process start
				if bm.verbose {
					fmt.Printf("New process detected: PID %d\n", pid)
				}
			}
			
			// Update process information
			bm.updateProcessInfo(proc)
		}
	}

	// Mark ended processes
	for pid, procInfo := range bm.processes {
		if !activePIDs[pid] && procInfo.EndTime == nil {
			now := time.Now()
			procInfo.EndTime = &now
			procInfo.Status = "completed"
			
			// Create end event
			event := BuildEvent{
				Timestamp: now,
				Type:      "process_end",
				Process:   *procInfo,
			}
			bm.events = append(bm.events, event)
			bm.broadcastEvent(event)
			
			if bm.verbose {
				fmt.Printf("Process ended: PID %d (%s)\n", pid, procInfo.Name)
			}
		}
	}
}

// isDescendant checks if pid is a descendant of rootPID
func (bm *BuildMonitor) isDescendant(pid, rootPID int32) bool {
	if pid == rootPID {
		return true
	}
	
	proc, err := process.NewProcess(pid)
	if err != nil {
		return false
	}
	
	ppid, err := proc.Ppid()
	if err != nil {
		return false
	}
	
	if ppid == rootPID {
		return true
	}
	
	// Recursively check parent chain
	return bm.isDescendant(ppid, rootPID)
}

// updateProcessInfo updates detailed information for a process
func (bm *BuildMonitor) updateProcessInfo(proc *process.Process) {
	pid := proc.Pid
	procInfo := bm.processes[pid]
	
	// Update basic info
	if name, err := proc.Name(); err == nil {
		procInfo.Name = name
	}
	
	if cmdline, err := proc.Cmdline(); err == nil {
		procInfo.Cmdline = cmdline
	}
	
	if ppid, err := proc.Ppid(); err == nil {
		procInfo.PPID = ppid
	}
	
	// Update resource usage
	if cpuPercent, err := proc.CPUPercent(); err == nil {
		procInfo.CPUUsage = cpuPercent
	}
	
	if memInfo, err := proc.MemoryInfo(); err == nil {
		procInfo.MemUsage = memInfo.RSS
	}
	
	if children, err := proc.Children(); err == nil {
		procInfo.Children = make([]int32, len(children))
		for i, child := range children {
			procInfo.Children[i] = child.Pid
		}
	}
	
	procInfo.Status = "running"
	
	// Create resource update event
	event := BuildEvent{
		Timestamp: time.Now(),
		Type:      "resource_update",
		Process:   *procInfo,
	}
	bm.events = append(bm.events, event)
	bm.broadcastEvent(event)
}

// broadcastEvent sends an event to all WebSocket subscribers
func (bm *BuildMonitor) broadcastEvent(event BuildEvent) {
	for i := len(bm.subscribers) - 1; i >= 0; i-- {
		select {
		case bm.subscribers[i] <- event:
		default:
			// Remove disconnected subscribers
			close(bm.subscribers[i])
			bm.subscribers = append(bm.subscribers[:i], bm.subscribers[i+1:]...)
		}
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

// StartWebServer starts the web server for real-time visualization
func (bm *BuildMonitor) StartWebServer(port int) {
	http.HandleFunc("/ws", bm.handleWebSocket)
	http.HandleFunc("/api/processes", bm.handleProcesses)
	http.HandleFunc("/api/events", bm.handleEvents)
	http.HandleFunc("/", bm.handleIndex)
	
	fmt.Printf("Web server starting on http://localhost:%d\n", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		fmt.Printf("Web server error: %v\n", err)
	}
}

// handleWebSocket handles WebSocket connections for real-time updates
func (bm *BuildMonitor) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Create subscriber channel
	eventChan := make(chan BuildEvent, 100)
	bm.subscribers = append(bm.subscribers, eventChan)

	// Send existing events
	bm.mutex.RLock()
	for _, event := range bm.events {
		conn.WriteJSON(event)
	}
	bm.mutex.RUnlock()

	// Send real-time events
	for event := range eventChan {
		if err := conn.WriteJSON(event); err != nil {
			break
		}
	}
}

// handleProcesses returns current process information
func (bm *BuildMonitor) handleProcesses(w http.ResponseWriter, r *http.Request) {
	bm.mutex.RLock()
	defer bm.mutex.RUnlock()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bm.processes)
}

// handleEvents returns the timeline of events
func (bm *BuildMonitor) handleEvents(w http.ResponseWriter, r *http.Request) {
	bm.mutex.RLock()
	defer bm.mutex.RUnlock()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bm.events)
}

// handleIndex serves the visualization dashboard
func (bm *BuildMonitor) handleIndex(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html>
<head>
    <title>BuildSpy - Real-time Build Process Monitor</title>
    <script src="https://d3js.org/d3.v7.min.js"></script>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; background: #1a1a1a; color: #fff; }
        .container { max-width: 1200px; margin: 0 auto; }
        .status { background: #2d2d2d; padding: 10px; border-radius: 5px; margin-bottom: 20px; }
        .section { background: #2d2d2d; border-radius: 8px; padding: 20px; margin: 20px 0; }
        .flame-graph { min-height: 400px; border: 1px solid #444; border-radius: 5px; overflow: hidden; }
        .timeline { max-height: 300px; overflow-y: auto; }
        .process-rect { cursor: pointer; }
        .process-rect:hover { stroke-width: 3px; }
        table { width: 100%; border-collapse: collapse; }
        th, td { border: 1px solid #444; padding: 8px; text-align: left; }
        th { background: #3d3d3d; }
        .running { background: #4CAF50; }
        .completed { background: #FF9800; }
        ul { list-style: none; padding: 0; }
        li { background: #3d3d3d; margin: 2px 0; padding: 8px; border-radius: 3px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔥 BuildSpy - Real-time Build Process Monitor</h1>
        <div id="status" class="status">Connecting...</div>
        
        <div class="section">
            <h3>Flame Graph - Process Hierarchy</h3>
            <div id="flame-graph" class="flame-graph"></div>
        </div>
        
        <div class="section">
            <h3>Recent Events</h3>
            <div id="timeline" class="timeline"></div>
        </div>
        
        <div class="section">
            <div id="process-details"></div>
        </div>
    </div>

    <script>
        const ws = new WebSocket('ws://' + location.host + '/ws');
        const processes = new Map();
        let events = [];
        let processHierarchy = {};

        ws.onopen = () => {
            document.getElementById('status').textContent = '✅ Connected - Monitoring active';
            document.getElementById('status').style.background = '#4CAF50';
        };
        
        ws.onclose = () => {
            document.getElementById('status').textContent = '❌ Disconnected';
            document.getElementById('status').style.background = '#F44336';
        };
        
        ws.onerror = () => {
            document.getElementById('status').textContent = '⚠️ Connection Error';
            document.getElementById('status').style.background = '#FF9800';
        };

        ws.onmessage = (event) => {
            const data = JSON.parse(event.data);
            events.push(data);
            processes.set(data.process.pid, data.process);
            buildProcessHierarchy();
            updateVisualization();
        };

        function buildProcessHierarchy() {
            processHierarchy = {};
            const processArray = Array.from(processes.values());
            
            // Build parent-child relationships
            processArray.forEach(proc => {
                if (!processHierarchy[proc.ppid]) {
                    processHierarchy[proc.ppid] = [];
                }
                processHierarchy[proc.ppid].push(proc);
            });
        }

        function updateVisualization() {
            updateFlameGraph();
            updateTimeline();
            updateProcessDetails();
        }

        function updateFlameGraph() {
            const container = d3.select('#flame-graph');
            container.selectAll('*').remove();
            
            const width = 800;
            const height = 400;
            const svg = container.append('svg')
                .attr('width', width)
                .attr('height', height);

            const processArray = Array.from(processes.values()).filter(p => p.status === 'running' || p.end_time);
            
            if (processArray.length === 0) {
                svg.append('text')
                    .attr('x', width/2)
                    .attr('y', height/2)
                    .attr('text-anchor', 'middle')
                    .style('fill', '#666')
                    .style('font-size', '18px')
                    .text('No processes detected yet...');
                return;
            }

            // Calculate positions based on hierarchy and timing
            const maxDepth = Math.max(...processArray.map(p => getProcessDepth(p.pid))) || 1;
            const rectHeight = Math.min(40, (height - 40) / maxDepth);
            const rectWidth = Math.max(60, (width - 100) / processArray.length);

            const rects = svg.selectAll('rect')
                .data(processArray)
                .enter()
                .append('g')
                .attr('class', 'process-group');

            // Draw process rectangles
            rects.append('rect')
                .attr('class', 'process-rect')
                .attr('x', (d, i) => 50 + (i * rectWidth))
                .attr('y', d => 20 + (getProcessDepth(d.pid) * rectHeight))
                .attr('width', rectWidth - 2)
                .attr('height', rectHeight - 2)
                .attr('fill', d => {
                    if (d.status === 'running') return '#4CAF50';
                    if (d.status === 'completed') return '#FF9800';
                    return '#2196F3';
                })
                .attr('stroke', '#333')
                .attr('stroke-width', 1)
                .on('mouseover', function(event, d) {
                    d3.select(this).attr('stroke-width', 3);
                    showTooltip(event, d);
                })
                .on('mouseout', function(event, d) {
                    d3.select(this).attr('stroke-width', 1);
                    hideTooltip();
                });

            // Add process labels
            rects.append('text')
                .attr('x', (d, i) => 50 + (i * rectWidth) + rectWidth/2)
                .attr('y', d => 20 + (getProcessDepth(d.pid) * rectHeight) + rectHeight/2)
                .attr('text-anchor', 'middle')
                .attr('dominant-baseline', 'central')
                .style('fill', 'white')
                .style('font-size', '10px')
                .style('pointer-events', 'none')
                .text(d => d.name.length > 8 ? d.name.substring(0, 8) + '...' : d.name);

            // Add PID labels
            rects.append('text')
                .attr('x', (d, i) => 50 + (i * rectWidth) + rectWidth/2)
                .attr('y', d => 20 + (getProcessDepth(d.pid) * rectHeight) + rectHeight/2 + 12)
                .attr('text-anchor', 'middle')
                .style('fill', '#ccc')
                .style('font-size', '8px')
                .style('pointer-events', 'none')
                .text(d => d.pid);
        }

        function getProcessDepth(pid) {
            const proc = processes.get(pid);
            if (!proc || proc.ppid === 0) return 0;
            if (processes.has(proc.ppid)) {
                return 1 + getProcessDepth(proc.ppid);
            }
            return 1;
        }

        function showTooltip(event, d) {
            const tooltip = d3.select('body').append('div')
                .attr('id', 'tooltip')
                .style('position', 'absolute')
                .style('background', '#333')
                .style('color', 'white')
                .style('padding', '10px')
                .style('border-radius', '5px')
                .style('pointer-events', 'none')
                .style('opacity', 0);

            tooltip.html(` + "`" + `
                <strong>${d.name}</strong><br/>
                PID: ${d.pid}<br/>
                PPID: ${d.ppid}<br/>
                CPU: ${d.cpu_usage.toFixed(2)}%<br/>
                Memory: ${Math.round(d.mem_usage / 1024)}KB<br/>
                Status: ${d.status}<br/>
                Command: ${d.cmdline.substring(0, 50)}...
            ` + "`" + `)
            .style('left', (event.pageX + 10) + 'px')
            .style('top', (event.pageY - 10) + 'px')
            .transition()
            .style('opacity', 1);
        }

        function hideTooltip() {
            d3.select('#tooltip').remove();
        }

        function updateTimeline() {
            const timeline = d3.select('#timeline');
            timeline.selectAll('*').remove();
            
            const list = timeline.append('ul');
            
            events.slice(-15).reverse().forEach(event => {
                const item = list.append('li');
                const timestamp = new Date(event.timestamp).toLocaleTimeString();
                const eventType = event.type.replace('_', ' ').toUpperCase();
                
                item.html(` + "`" + `
                    <strong>${timestamp}</strong> - 
                    <span style="color: ${getEventColor(event.type)};">${eventType}</span>: 
                    ${event.process.name} (PID: ${event.process.pid})
                    ${event.process.cpu_usage > 0 ? ' - CPU: ' + event.process.cpu_usage.toFixed(1) + '%' : ''}
                ` + "`" + `);
            });
        }

        function getEventColor(eventType) {
            switch(eventType) {
                case 'process_start': return '#4CAF50';
                case 'process_end': return '#FF9800';
                case 'resource_update': return '#2196F3';
                case 'build_complete': return '#9C27B0';
                default: return '#FFF';
            }
        }

        function updateProcessDetails() {
            const details = d3.select('#process-details');
            details.selectAll('*').remove();
            
            const activeProcesses = Array.from(processes.values()).filter(p => p.status === 'running');
            
            details.append('h3').text(` + "`" + `Active Processes (${activeProcesses.length})` + "`" + `);
            
            if (activeProcesses.length === 0) {
                details.append('p').style('color', '#666').text('No active processes');
                return;
            }
            
            const table = details.append('table');
            
            // Header
            const header = table.append('thead').append('tr');
            ['PID', 'Name', 'CPU %', 'Memory (KB)', 'Threads', 'Duration'].forEach(col => {
                header.append('th').text(col);
            });
            
            // Rows
            const tbody = table.append('tbody');
            activeProcesses
                .sort((a, b) => b.cpu_usage - a.cpu_usage) // Sort by CPU usage
                .forEach(proc => {
                    const row = tbody.append('tr');
                    const duration = proc.start_time ? 
                        Math.round((Date.now() - new Date(proc.start_time)) / 1000) : 0;
                    
                    [
                        proc.pid,
                        proc.name,
                        proc.cpu_usage.toFixed(1),
                        Math.round(proc.mem_usage / 1024),
                        proc.children ? proc.children.length : 0,
                        duration + 's'
                    ].forEach((val, i) => {
                        const cell = row.append('td').text(val);
                        if (i === 2 && parseFloat(val) > 50) { // High CPU usage
                            cell.style('color', '#FF5722');
                        }
                    });
                });
        }

        // Initialize
        updateVisualization();
        
        // Periodic updates for duration calculation
        setInterval(() => {
            if (processes.size > 0) {
                updateProcessDetails();
            }
        }, 1000);
    </script>
</body>
</html>`
	
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// WaitForExit waits for interrupt signal or until all WebSocket connections close
func (bm *BuildMonitor) WaitForExit() {
	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	
	// Wait for either signal or when no active subscribers
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-sigChan:
			fmt.Printf("\nReceived interrupt signal. Shutting down...\n")
			bm.cancel()
			return
		case <-ticker.C:
			bm.mutex.RLock()
			buildDone := bm.buildDone
			subscriberCount := len(bm.subscribers)
			bm.mutex.RUnlock()
			
			// If build is done and no active WebSocket connections, we can exit
			if buildDone && subscriberCount == 0 {
				if bm.verbose {
					fmt.Printf("Build completed and no active connections. Shutting down...\n")
				}
				bm.cancel()
				return
			}
			
			if bm.verbose && buildDone && subscriberCount > 0 {
				fmt.Printf("Build completed. Waiting for %d WebSocket connection(s) to close...\n", subscriberCount)
			}
		}
	}
}