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
	ticker := time.NewTicker(50 * time.Millisecond) // Higher frequency for better responsiveness
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
	
	// Only send resource updates for significant changes to reduce noise
	if procInfo.CPUUsage > 5.0 || len(procInfo.Children) > 0 {
		event := BuildEvent{
			Timestamp: time.Now(),
			Type:      "resource_update",
			Process:   *procInfo,
		}
		bm.events = append(bm.events, event)
		bm.broadcastEvent(event)
	}
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
        @keyframes pulse {
            0% { transform: scale(1); box-shadow: 0 0 0 0 rgba(76, 175, 80, 0.7); }
            50% { transform: scale(1.05); box-shadow: 0 0 0 10px rgba(76, 175, 80, 0); }
            100% { transform: scale(1); box-shadow: 0 0 0 0 rgba(76, 175, 80, 0); }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔥 BuildSpy - Real-time Build Process Monitor</h1>
        <div id="status" class="status">Connecting...</div>
        <div id="build-status" class="status" style="display: none;">
            <span id="build-indicator">🔨</span>
            <span id="build-text">Build in progress...</span>
        </div>
        
        <div class="section">
            <h3>⏱️ Process Timeline - Real-time Build Visualization</h3>
            <div id="flame-graph" class="flame-graph"></div>
        </div>
        
        <div class="section">
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
            document.getElementById('build-status').style.display = 'block';
        };
        
        ws.onclose = () => {
            document.getElementById('status').textContent = '❌ Disconnected';
            document.getElementById('status').style.background = '#F44336';
        };
        
        ws.onerror = () => {
            document.getElementById('status').textContent = '⚠️ Connection Error';
            document.getElementById('status').style.background = '#FF9800';
        };

        // Build state tracking
        let buildCompleted = false;
        let buildStatus = 'running';
        
        // Throttle updates to improve performance
        let updatePending = false;
        
        ws.onmessage = (event) => {
            const data = JSON.parse(event.data);
            events.push(data);
            processes.set(data.process.pid, data.process);
            
            // Check for build completion
            if (data.type === 'build_complete') {
                buildCompleted = true;
                buildStatus = 'completed';
                updateBuildStatus();
            }
            
            // Throttle visualization updates
            if (!updatePending) {
                updatePending = true;
                setTimeout(() => {
                    buildProcessHierarchy();
                    updateVisualization();
                    updatePending = false;
                }, 250); // Update at most 4 times per second
            }
        };

        function updateBuildStatus() {
            const buildStatusDiv = document.getElementById('build-status');
            const buildIndicator = document.getElementById('build-indicator');
            const buildText = document.getElementById('build-text');
            
            if (buildCompleted) {
                buildIndicator.textContent = '✅';
                buildText.textContent = 'Build completed successfully!';
                buildStatusDiv.style.background = '#4CAF50';
                buildStatusDiv.style.border = '2px solid #66BB6A';
                
                // Add celebration effect
                buildStatusDiv.style.animation = 'pulse 2s ease-in-out 3';
            } else {
                const activeProcs = Array.from(processes.values()).filter(p => p.status === 'running');
                buildIndicator.textContent = '🔨';
                buildText.textContent = 'Build in progress... (' + activeProcs.length + ' active processes)';
                buildStatusDiv.style.background = '#2196F3';
                buildStatusDiv.style.border = '2px solid #42A5F5';
            }
        }

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

        // Timeline visualization state
        let timelineData = [];
        let startTime = null;
        
        function updateVisualization() {
            updateTimelineChart();
            updateEventLog();
            updateProcessSummary();
        }

        function updateTimelineChart() {
            const container = d3.select('#flame-graph');
            container.selectAll('*').remove();
            
            const width = 1000;
            const height = 400;
            const margin = {top: 20, right: 50, bottom: 40, left: 100};
            
            const svg = container.append('svg')
                .attr('width', width)
                .attr('height', height);

            // Build timeline data from events
            if (events.length === 0) {
                svg.append('text')
                    .attr('x', width/2)
                    .attr('y', height/2)
                    .attr('text-anchor', 'middle')
                    .style('fill', '#666')
                    .style('font-size', '18px')
                    .text('Waiting for build to start...');
                return;
            }

            // Set start time from first event
            if (!startTime) {
                startTime = new Date(events[0].timestamp);
            }

            // Build timeline segments for each process
            const processTimeline = new Map();
            events.forEach(event => {
                const pid = event.process.pid;
                const timestamp = new Date(event.timestamp);
                
                if (!processTimeline.has(pid)) {
                    processTimeline.set(pid, {
                        pid: pid,
                        name: event.process.name,
                        start: timestamp,
                        end: null,
                        maxCpu: 0,
                        status: event.process.status
                    });
                }
                
                const proc = processTimeline.get(pid);
                if (event.type === 'process_end') {
                    proc.end = timestamp;
                    proc.status = 'completed';
                }
                if (event.process.cpu_usage > proc.maxCpu) {
                    proc.maxCpu = event.process.cpu_usage;
                }
            });

            const timelineArray = Array.from(processTimeline.values())
                .filter(p => p.start)
                .sort((a, b) => a.start - b.start);

            if (timelineArray.length === 0) return;

            // Calculate time scale
            const now = new Date();
            const endTime = Math.max(...timelineArray.map(p => p.end ? p.end.getTime() : now.getTime()));
            const duration = endTime - startTime.getTime();
            
            const xScale = d3.scaleLinear()
                .domain([0, duration])
                .range([margin.left, width - margin.right]);

            const yScale = d3.scaleBand()
                .domain(timelineArray.map((d, i) => i))
                .range([margin.top, height - margin.bottom])
                .padding(0.1);

            // Draw timeline bars
            const bars = svg.selectAll('.timeline-bar')
                .data(timelineArray)
                .enter()
                .append('g')
                .attr('class', 'timeline-bar');

            bars.append('rect')
                .attr('x', d => xScale(d.start.getTime() - startTime.getTime()))
                .attr('y', (d, i) => yScale(i))
                .attr('width', d => {
                    const end = d.end ? d.end.getTime() : now.getTime();
                    const w = xScale(end - startTime.getTime()) - xScale(d.start.getTime() - startTime.getTime());
                    return Math.max(2, w);
                })
                .attr('height', yScale.bandwidth())
                .attr('fill', d => {
                    if (d.status === 'running') return '#4CAF50';
                    if (d.status === 'completed') return '#FF9800';
                    return '#2196F3';
                })
                .attr('opacity', d => Math.min(0.9, 0.3 + (d.maxCpu / 100) * 0.6))
                .on('mouseover', function(event, d) {
                    // Remove any existing tooltips first
                    d3.selectAll('#tooltip').remove();
                    showProcessTooltip(event, d);
                })
                .on('mouseout', function(event, d) {
                    hideTooltip();
                });

            // Add process labels
            bars.append('text')
                .attr('x', margin.left - 5)
                .attr('y', (d, i) => yScale(i) + yScale.bandwidth()/2)
                .attr('text-anchor', 'end')
                .attr('dominant-baseline', 'middle')
                .style('fill', '#ccc')
                .style('font-size', '11px')
                .text(d => d.name + ' (' + d.pid + ')');

            // Add time axis
            const timeAxis = d3.axisBottom(xScale)
                .tickFormat(d => Math.round(d/1000) + 's');
                
            svg.append('g')
                .attr('transform', 'translate(0, ' + (height - margin.bottom) + ')')
                .style('color', '#ccc')
                .call(timeAxis);

            // Add current time indicator if build is running, or completion marker if done
            const activeProcs = Array.from(processes.values()).filter(p => p.status === 'running');
            const currentX = xScale(now.getTime() - startTime.getTime());
            
            if (buildCompleted) {
                // Build completion marker
                svg.append('line')
                    .attr('x1', currentX)
                    .attr('x2', currentX)
                    .attr('y1', margin.top)
                    .attr('y2', height - margin.bottom)
                    .attr('stroke', '#4CAF50')
                    .attr('stroke-width', 3)
                    .attr('opacity', 0.9);
                    
                svg.append('text')
                    .attr('x', currentX + 5)
                    .attr('y', margin.top + 15)
                    .attr('fill', '#4CAF50')
                    .style('font-size', '12px')
                    .style('font-weight', 'bold')
                    .text('BUILD COMPLETE ✅');
            } else if (activeProcs.length > 0) {
                // Current time indicator for running build
                svg.append('line')
                    .attr('x1', currentX)
                    .attr('x2', currentX)
                    .attr('y1', margin.top)
                    .attr('y2', height - margin.bottom)
                    .attr('stroke', '#FF5722')
                    .attr('stroke-width', 2)
                    .attr('opacity', 0.8);
                    
                svg.append('text')
                    .attr('x', currentX + 5)
                    .attr('y', margin.top + 15)
                    .attr('fill', '#FF5722')
                    .style('font-size', '12px')
                    .text('NOW');
            }
        }

        function getProcessDepth(pid) {
            const proc = processes.get(pid);
            if (!proc || proc.ppid === 0) return 0;
            if (processes.has(proc.ppid)) {
                return 1 + getProcessDepth(proc.ppid);
            }
            return 1;
        }

        function showProcessTooltip(event, d) {
            // Always remove existing tooltips first
            d3.selectAll('#tooltip').remove();
            
            const tooltip = d3.select('body').append('div')
                .attr('id', 'tooltip')
                .style('position', 'absolute')
                .style('background', '#333')
                .style('color', 'white')
                .style('padding', '10px')
                .style('border-radius', '5px')
                .style('pointer-events', 'none')
                .style('z-index', '9999')
                .style('border', '1px solid #555')
                .style('box-shadow', '0 4px 8px rgba(0,0,0,0.3)')
                .style('opacity', 0);

            const duration = d.end ? (d.end.getTime() - d.start.getTime()) / 1000 : 
                             (Date.now() - d.start.getTime()) / 1000;

            const endText = d.end ? '<br/>End: ' + d.end.toLocaleTimeString() : '<br/>Still running...';

            tooltip.html(
                '<strong>' + d.name + '</strong><br/>' +
                'PID: ' + d.pid + '<br/>' +
                'Duration: ' + duration.toFixed(2) + 's<br/>' +
                'Max CPU: ' + d.maxCpu.toFixed(1) + '%<br/>' +
                'Status: ' + d.status + '<br/>' +
                'Start: ' + d.start.toLocaleTimeString() + 
                endText
            )
            .style('left', (event.pageX + 10) + 'px')
            .style('top', (event.pageY - 10) + 'px')
            .transition()
            .duration(150)
            .style('opacity', 1);
        }

        function hideTooltip() {
            d3.selectAll('#tooltip').transition().duration(200).style('opacity', 0).remove();
        }

        function updateEventLog() {
            const timeline = d3.select('#timeline');
            timeline.selectAll('*').remove();
            
            timeline.append('h4').text('Recent Events').style('color', '#ccc');
            const list = timeline.append('ul');
            
            events.slice(-20).reverse().forEach(event => {
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

        function updateProcessSummary() {
            const details = d3.select('#process-details');
            details.selectAll('*').remove();
            
            const activeProcesses = Array.from(processes.values()).filter(p => p.status === 'running');
            const totalProcesses = processes.size;
            const completedProcesses = totalProcesses - activeProcesses.length;
            
            // Summary stats
            details.append('h3').text('Build Summary');
            
            const statsDiv = details.append('div').style('display', 'flex').style('gap', '20px').style('margin-bottom', '20px');
            
            statsDiv.append('div')
                .style('background', '#2d2d2d')
                .style('padding', '10px')
                .style('border-radius', '5px')
                .html(` + "`" + `<strong>Total Processes</strong><br/><span style="font-size: 24px; color: #2196F3;">${totalProcesses}</span>` + "`" + `);
            
            statsDiv.append('div')
                .style('background', '#2d2d2d')
                .style('padding', '10px')
                .style('border-radius', '5px')
                .html(` + "`" + `<strong>Active</strong><br/><span style="font-size: 24px; color: #4CAF50;">${activeProcesses.length}</span>` + "`" + `);
            
            statsDiv.append('div')
                .style('background', '#2d2d2d')
                .style('padding', '10px')
                .style('border-radius', '5px')
                .html(` + "`" + `<strong>Completed</strong><br/><span style="font-size: 24px; color: #FF9800;">${completedProcesses}</span>` + "`" + `);
            
            // Active processes table (only if there are any)
            if (activeProcesses.length > 0) {
                details.append('h4').text('Currently Running').style('color', '#ccc');
                
                const table = details.append('table');
                
                // Header
                const header = table.append('thead').append('tr');
                ['PID', 'Name', 'CPU %', 'Memory (KB)', 'Duration'].forEach(col => {
                    header.append('th').text(col);
                });
                
                // Rows
                const tbody = table.append('tbody');
                activeProcesses
                    .sort((a, b) => b.cpu_usage - a.cpu_usage) // Sort by CPU usage
                    .slice(0, 10) // Show only top 10
                    .forEach(proc => {
                        const row = tbody.append('tr');
                        const duration = proc.start_time ? 
                            Math.round((Date.now() - new Date(proc.start_time)) / 1000) : 0;
                        
                        [
                            proc.pid,
                            proc.name.length > 15 ? proc.name.substring(0, 15) + '...' : proc.name,
                            proc.cpu_usage.toFixed(1),
                            Math.round(proc.mem_usage / 1024),
                            duration + 's'
                        ].forEach((val, i) => {
                            const cell = row.append('td').text(val);
                            if (i === 2 && parseFloat(val) > 50) { // High CPU usage
                                cell.style('color', '#FF5722');
                            }
                        });
                    });
            }
        }

        // Initialize
        updateVisualization();
        
        // Periodic updates for duration calculation and timeline refresh
        setInterval(() => {
            if (processes.size > 0) {
                updateProcessSummary();
                updateBuildStatus(); // Update build status regularly
                
                // Also refresh timeline to update running process durations
                const activeProcs = Array.from(processes.values()).filter(p => p.status === 'running');
                if (activeProcs.length > 0) {
                    updateTimelineChart();
                }
            }
        }, 1000);
        
        // Global mouse handler to hide tooltips when moving away from chart
        d3.select('body').on('mouseover', function(event) {
            // If mouse is not over a timeline bar or tooltip, hide tooltips
            if (!event.target.closest('.timeline-bar') && !event.target.closest('#tooltip')) {
                const tooltip = d3.select('#tooltip');
                if (!tooltip.empty()) {
                    setTimeout(() => {
                        if (!d3.select('#tooltip').empty()) {
                            hideTooltip();
                        }
                    }, 100); // Small delay to prevent flicker
                }
            }
        });
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