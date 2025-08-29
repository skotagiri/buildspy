package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"buildspy/config"
	"buildspy/database"
	"buildspy/models"
)

// BuildspyDaemon serves historical build data and provides real-time monitoring
type BuildspyDaemon struct {
	config      *config.DaemonConfig
	database    *database.Database
	server      *http.Server
	subscribers map[string][]chan interface{} // WebSocket subscribers by type
	liveBuilds  map[string]*LiveBuildMonitor  // Currently running builds
}

// LiveBuildMonitor tracks a currently running build for real-time updates
type LiveBuildMonitor struct {
	BuildRunID string
	StartTime  time.Time
	LastSeen   time.Time
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

func main() {
	var (
		dataDir = flag.String("data", config.DefaultDataDir(), "Data directory containing build metrics")
		port    = flag.Int("port", 8080, "HTTP server port")
		verbose = flag.Bool("verbose", false, "Verbose logging")
	)
	flag.Parse()

	cfg := &config.DaemonConfig{
		DataDir: *dataDir,
		Port:    *port,
		Verbose: *verbose,
	}

	daemon, err := NewBuildspyDaemon(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize daemon: %v", err)
	}
	defer daemon.Close()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start server in background
	go func() {
		if err := daemon.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	if cfg.Verbose {
		fmt.Printf("BuildSpy daemon started on http://localhost:%d\n", cfg.Port)
		fmt.Printf("Data directory: %s\n", cfg.DataDir)
	}

	// Wait for shutdown signal
	<-sigChan
	if cfg.Verbose {
		fmt.Println("\nShutting down daemon...")
	}

	daemon.Shutdown()
}

func NewBuildspyDaemon(cfg *config.DaemonConfig) (*BuildspyDaemon, error) {
	// Initialize database
	db, err := database.NewDatabase(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	daemon := &BuildspyDaemon{
		config:      cfg,
		database:    db,
		subscribers: make(map[string][]chan interface{}),
		liveBuilds:  make(map[string]*LiveBuildMonitor),
	}

	// Set up HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", daemon.handleIndex)
	mux.HandleFunc("/api/runs", daemon.handleRuns)
	mux.HandleFunc("/api/runs/", daemon.handleRunDetails)
	mux.HandleFunc("/api/live", daemon.handleLiveBuilds)
	mux.HandleFunc("/api/stats", daemon.handleStats)
	mux.HandleFunc("/api/live/submit", daemon.handleLiveSubmit)
	mux.HandleFunc("/api/delete", daemon.handleDelete)
	mux.HandleFunc("/ws/live", daemon.handleLiveWebSocket)
	mux.HandleFunc("/ws/runs/", daemon.handleRunWebSocket)

	daemon.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}

	// Start background tasks
	go daemon.backgroundTasks()

	return daemon, nil
}

func (d *BuildspyDaemon) Start() error {
	return d.server.ListenAndServe()
}

func (d *BuildspyDaemon) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	d.server.Shutdown(ctx)
}

func (d *BuildspyDaemon) Close() {
	if d.database != nil {
		d.database.Close()
	}
}

func (d *BuildspyDaemon) backgroundTasks() {
	ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds for faster crash detection
	defer ticker.Stop()

	for range ticker.C {
		// Update live builds status
		d.updateLiveBuilds()
	}
}

func (d *BuildspyDaemon) updateLiveBuilds() {
	// Check for builds that haven't sent updates recently (assume crashed/stopped)
	cutoff := time.Now().Add(-30 * time.Second) // Reduced from 5 minutes to 30 seconds
	for runID, monitor := range d.liveBuilds {
		if monitor.LastSeen.Before(cutoff) {
			// Mark build as crashed/failed in database
			if build, err := d.database.GetBuildRun(runID); err == nil {
				build.Status = "failed"
				exitCode := -1 // Special exit code indicating crash/timeout
				build.ExitCode = &exitCode
				endTime := time.Now()
				build.EndTime = &endTime
				duration := endTime.Sub(build.StartTime).Milliseconds()
				build.Duration = &duration
				
				if err := d.database.SaveBuildRun(build); err != nil && d.config.Verbose {
					fmt.Printf("Warning: failed to mark crashed build %s as failed: %v\n", runID, err)
				}
				
				// Create a build failure event
				crashEvent := models.NewBuildEvent(runID, "build_crashed", models.ProcessInfo{
					BuildRunID: runID,
					Status:     "crashed",
					Name:       "monitor",
				})
				
				if err := d.database.SaveBuildEvent(crashEvent); err != nil && d.config.Verbose {
					fmt.Printf("Warning: failed to save crash event for build %s: %v\n", runID, err)
				}
				
				// Broadcast crash event
				d.broadcastEvent("live", *crashEvent)
				d.broadcastEvent("run:"+runID, *crashEvent)
			}
			
			delete(d.liveBuilds, runID)
			if d.config.Verbose {
				fmt.Printf("Live build %s marked as crashed/failed due to monitor timeout\n", runID)
			}
		}
	}
}

// HTTP Handlers
func (d *BuildspyDaemon) handleRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		d.handleListRuns(w, r)
	case "DELETE":
		d.handleDeleteRun(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *BuildspyDaemon) handleListRuns(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	query := r.URL.Query()
	search := query.Get("search")
	limitStr := query.Get("limit")
	
	limit := 50 // default limit
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	var runs []*models.BuildRun
	var err error
	
	if search != "" {
		runs, err = d.database.SearchBuildRuns(search)
	} else {
		runs, err = d.database.ListBuildRuns()
	}
	
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply limit
	if len(runs) > limit {
		runs = runs[:limit]
	}

	// Filter out live builds from the normal runs list
	var filteredRuns []*models.BuildRun
	for _, run := range runs {
		if _, isLive := d.liveBuilds[run.ID]; !isLive {
			filteredRuns = append(filteredRuns, run)
		}
	}
	runs = filteredRuns

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}

func (d *BuildspyDaemon) handleLiveBuilds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		d.handleListLiveBuilds(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *BuildspyDaemon) handleListLiveBuilds(w http.ResponseWriter, r *http.Request) {
	var liveRuns []*models.BuildRun
	
	// Get all live builds from memory
	for runID := range d.liveBuilds {
		// Fetch the build run from database
		run, err := d.database.GetBuildRun(runID)
		if err != nil {
			if d.config.Verbose {
				fmt.Printf("Warning: failed to fetch live build %s: %v\n", runID, err)
			}
			continue
		}
		
		run.Status = "running_live"
		liveRuns = append(liveRuns, run)
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(liveRuns)
}

func (d *BuildspyDaemon) handleRunDetails(w http.ResponseWriter, r *http.Request) {
	// Extract run ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		http.Error(w, "Missing run ID", http.StatusBadRequest)
		return
	}
	
	runID := parts[0]
	resource := ""
	if len(parts) > 1 {
		resource = parts[1]
	}

	switch r.Method {
	case "GET":
		switch resource {
		case "events":
			d.handleRunEvents(w, r, runID)
		case "processes":
			d.handleRunProcesses(w, r, runID)
		case "":
			d.handleRunInfo(w, r, runID)
		default:
			http.Error(w, "Unknown resource", http.StatusNotFound)
		}
	case "DELETE":
		if resource != "" {
			http.Error(w, "Cannot delete sub-resources", http.StatusBadRequest)
			return
		}
		d.handleDeleteSpecificRun(w, r, runID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *BuildspyDaemon) handleRunInfo(w http.ResponseWriter, r *http.Request, runID string) {
	run, err := d.database.GetBuildRun(runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Add live status
	if _, isLive := d.liveBuilds[runID]; isLive {
		run.Status = "running_live"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run)
}

func (d *BuildspyDaemon) handleRunEvents(w http.ResponseWriter, r *http.Request, runID string) {
	events, err := d.database.GetBuildEvents(runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (d *BuildspyDaemon) handleRunProcesses(w http.ResponseWriter, r *http.Request, runID string) {
	processes, err := d.database.GetProcesses(runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(processes)
}

func (d *BuildspyDaemon) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := d.database.GetStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Add live build count
	stats["live_builds"] = len(d.liveBuilds)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// HandleLiveSubmit receives live build data from CLI tools
func (d *BuildspyDaemon) handleLiveSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var submission struct {
		Type string      `json:"type"` // "build_run", "build_event", "process"
		Data interface{} `json:"data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	switch submission.Type {
	case "build_run":
		var buildRun models.BuildRun
		data, _ := json.Marshal(submission.Data)
		if err := json.Unmarshal(data, &buildRun); err != nil {
			http.Error(w, "Invalid build run data", http.StatusBadRequest)
			return
		}
		
		if err := d.database.SaveBuildRun(&buildRun); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		// Mark as live build
		d.liveBuilds[buildRun.ID] = &LiveBuildMonitor{
			BuildRunID: buildRun.ID,
			StartTime:  buildRun.StartTime,
			LastSeen:   time.Now(),
		}

	case "build_event":
		var buildEvent models.BuildEvent
		data, _ := json.Marshal(submission.Data)
		if err := json.Unmarshal(data, &buildEvent); err != nil {
			http.Error(w, "Invalid build event data", http.StatusBadRequest)
			return
		}
		
		if err := d.database.SaveBuildEvent(&buildEvent); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		// Handle build completion
		if buildEvent.Type == "build_complete" {
			if _, exists := d.liveBuilds[buildEvent.BuildRunID]; exists {
				delete(d.liveBuilds, buildEvent.BuildRunID)
				if d.config.Verbose {
					fmt.Printf("Build %s completed and moved from live to normal builds\n", buildEvent.BuildRunID)
				}
			}
		} else {
			// Update live build timestamp for other events
			if monitor, exists := d.liveBuilds[buildEvent.BuildRunID]; exists {
				monitor.LastSeen = time.Now()
			}
		}
		
		// Broadcast to WebSocket subscribers
		d.broadcastEvent("live", buildEvent)
		d.broadcastEvent("run:"+buildEvent.BuildRunID, buildEvent)

	case "process":
		var processInfo models.ProcessInfo
		data, _ := json.Marshal(submission.Data)
		if err := json.Unmarshal(data, &processInfo); err != nil {
			http.Error(w, "Invalid process data", http.StatusBadRequest)
			return
		}
		
		if err := d.database.SaveProcess(&processInfo); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

	default:
		http.Error(w, "Unknown submission type", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// WebSocket handlers
func (d *BuildspyDaemon) handleLiveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		if d.config.Verbose {
			fmt.Printf("WebSocket upgrade failed: %v\n", err)
		}
		return
	}
	defer conn.Close()

	// Create subscriber channel
	eventChan := make(chan interface{}, 100)
	d.addSubscriber("live", eventChan)
	defer d.removeSubscriber("live", eventChan)

	if d.config.Verbose {
		fmt.Println("New live WebSocket connection")
	}

	// Send current live builds
	runs, err := d.database.ListBuildRuns()
	if err == nil {
		// Filter for recent runs that might be live
		recentRuns := []*models.BuildRun{}
		cutoff := time.Now().Add(-1 * time.Hour)
		for _, run := range runs {
			if run.StartTime.After(cutoff) && run.Status == "running" {
				recentRuns = append(recentRuns, run)
			}
			if len(recentRuns) >= 10 {
				break
			}
		}
		
		conn.WriteJSON(map[string]interface{}{
			"type": "initial_state",
			"data": recentRuns,
		})
	}

	// Handle incoming messages and send real-time events
	for {
		select {
		case event := <-eventChan:
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		default:
			// Check for new messages from client
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}
}

func (d *BuildspyDaemon) handleRunWebSocket(w http.ResponseWriter, r *http.Request) {
	// Extract run ID from path
	path := strings.TrimPrefix(r.URL.Path, "/ws/runs/")
	runID := strings.Split(path, "/")[0]

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		if d.config.Verbose {
			fmt.Printf("WebSocket upgrade failed: %v\n", err)
		}
		return
	}
	defer conn.Close()

	if d.config.Verbose {
		fmt.Printf("New WebSocket connection for run %s\n", runID)
	}

	// Send historical data for the run
	events, err := d.database.GetBuildEvents(runID)
	if err == nil {
		for _, event := range events {
			conn.WriteJSON(event)
		}
	}

	// Create subscriber channel for this run
	eventChan := make(chan interface{}, 100)
	subscriberKey := "run:" + runID
	d.addSubscriber(subscriberKey, eventChan)
	defer d.removeSubscriber(subscriberKey, eventChan)

	// Handle real-time updates
	for {
		select {
		case event := <-eventChan:
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		default:
			// Check for new messages from client
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}
}

func (d *BuildspyDaemon) addSubscriber(key string, ch chan interface{}) {
	if d.subscribers[key] == nil {
		d.subscribers[key] = make([]chan interface{}, 0)
	}
	d.subscribers[key] = append(d.subscribers[key], ch)
}

func (d *BuildspyDaemon) removeSubscriber(key string, ch chan interface{}) {
	if channels, exists := d.subscribers[key]; exists {
		for i, c := range channels {
			if c == ch {
				d.subscribers[key] = append(channels[:i], channels[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

func (d *BuildspyDaemon) broadcastEvent(key string, event interface{}) {
	if channels, exists := d.subscribers[key]; exists {
		for i := len(channels) - 1; i >= 0; i-- {
			select {
			case channels[i] <- event:
			default:
				// Remove disconnected subscribers
				close(channels[i])
				d.subscribers[key] = append(channels[:i], channels[i+1:]...)
			}
		}
	}
}

// handleIndex serves the main web interface
func (d *BuildspyDaemon) handleIndex(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html>
<head>
    <title>BuildSpy Dashboard - Build Process Monitor</title>
    <script src="https://d3js.org/d3.v7.min.js"></script>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; background: #1a1a1a; color: #fff; }
        .container { max-width: 1400px; margin: 0 auto; }
        .header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 30px; }
        .status { background: #2d2d2d; padding: 15px; border-radius: 8px; margin-bottom: 20px; }
        .section { background: #2d2d2d; border-radius: 8px; padding: 20px; margin: 20px 0; }
        .runs-list { max-height: 600px; overflow-y: auto; }
        .run-item { 
            background: #3d3d3d; padding: 15px; margin: 10px 0; border-radius: 5px; 
            cursor: pointer; transition: background 0.2s;
        }
        .run-item:hover { background: #4d4d4d; }
        .run-item.selected { background: #2196F3; }
        .run-item.running { border-left: 4px solid #4CAF50; }
        .run-item.completed { border-left: 4px solid #FF9800; }
        .run-item.failed { border-left: 4px solid #F44336; }
        .run-meta { font-size: 0.9em; color: #ccc; margin-top: 8px; }
        .flame-graph { min-height: 500px; border: 1px solid #444; border-radius: 5px; overflow: hidden; }
        .timeline { max-height: 400px; overflow-y: auto; }
        .controls { display: flex; gap: 15px; align-items: center; margin-bottom: 20px; }
        .search-box { padding: 8px; border-radius: 4px; border: 1px solid #666; background: #2d2d2d; color: #fff; }
        .btn { 
            padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer;
            background: #2196F3; color: white; font-size: 14px;
        }
        .btn:hover { background: #1976D2; }
        .btn.active { background: #4CAF50; }
        .stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 15px; }
        .stat-card { background: #3d3d3d; padding: 15px; border-radius: 5px; text-align: center; }
        .stat-value { font-size: 24px; font-weight: bold; color: #2196F3; }
        table { width: 100%; border-collapse: collapse; }
        th, td { border: 1px solid #444; padding: 8px; text-align: left; }
        th { background: #3d3d3d; }
        ul { list-style: none; padding: 0; }
        li { background: #3d3d3d; margin: 2px 0; padding: 8px; border-radius: 3px; }
        .no-runs { text-align: center; padding: 40px; color: #666; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🔍 BuildSpy Dashboard</h1>
            <div class="controls">
                <input type="text" id="search" class="search-box" placeholder="Search builds...">
                <button id="refresh-btn" class="btn">Refresh</button>
                <button id="live-btn" class="btn">Live Mode</button>
            </div>
        </div>
        
        <div id="status" class="status">
            <div id="stats-container" class="stats-grid"></div>
        </div>

        <div class="section">
            <h3>🔴 Live Builds</h3>
            <div id="live-builds-list" class="runs-list"></div>
        </div>
        
        <div class="section">
            <h3>📋 Completed Builds</h3>
            <div id="runs-list" class="runs-list"></div>
        </div>
        
        <div id="details-section" class="section" style="display: none;">
            <h3 id="details-title">📊 Build Details</h3>
            <div id="flame-graph" class="flame-graph"></div>
            <div id="timeline" class="timeline"></div>
        </div>
    </div>

    <script>
        let selectedRunId = null;
        let liveMode = false;
        let liveWs = null;
        let runWs = null;

        // Initialize
        document.addEventListener('DOMContentLoaded', function() {
            loadStats();
            loadRuns();
            loadLiveBuilds();
            
            // Start auto-refresh for live builds every 2 seconds
            setInterval(() => {
                loadLiveBuilds();
                loadStats(); // Also refresh stats to show live build count
                
                // If a run is selected and it's a live build, refresh its details too
                if (selectedRunId) {
                    refreshSelectedRunIfLive();
                }
            }, 2000);
            
            document.getElementById('search').addEventListener('input', function(e) {
                setTimeout(() => loadRuns(e.target.value), 300);
            });
            
            document.getElementById('refresh-btn').addEventListener('click', function() {
                loadStats();
                loadRuns();
                loadLiveBuilds();
                if (selectedRunId) {
                    loadRunDetails(selectedRunId);
                }
            });
            
            document.getElementById('live-btn').addEventListener('click', toggleLiveMode);
        });

        async function loadStats() {
            try {
                const response = await fetch('/api/stats');
                const stats = await response.json();
                displayStats(stats);
            } catch (error) {
                console.error('Error loading stats:', error);
            }
        }

        function displayStats(stats) {
            const container = document.getElementById('stats-container');
            container.innerHTML = '';
            
            const statCards = [
                { label: 'Total Runs', value: stats.total_runs || 0, color: '#2196F3' },
                { label: 'Live Builds', value: stats.live_builds || 0, color: '#4CAF50' },
                { label: 'Completed', value: (stats.by_status && stats.by_status.completed) || 0, color: '#FF9800' },
                { label: 'Failed', value: (stats.by_status && stats.by_status.failed) || 0, color: '#F44336' }
            ];
            
            statCards.forEach(stat => {
                const card = document.createElement('div');
                card.className = 'stat-card';
                card.innerHTML = ` + "`" + `
                    <div class="stat-value" style="color: ${stat.color}">${stat.value}</div>
                    <div>${stat.label}</div>
                ` + "`" + `;
                container.appendChild(card);
            });
        }

        async function loadRuns(search = '') {
            try {
                const url = search ? ` + "`" + `/api/runs?search=${encodeURIComponent(search)}` + "`" + ` : '/api/runs';
                const response = await fetch(url);
                const runs = await response.json();
                displayRuns(runs);
            } catch (error) {
                console.error('Error loading runs:', error);
            }
        }

        async function loadLiveBuilds() {
            try {
                const response = await fetch('/api/live');
                const liveBuilds = await response.json();
                displayLiveBuilds(liveBuilds || []);
            } catch (error) {
                console.error('Error loading live builds:', error);
            }
        }

        async function refreshSelectedRunIfLive() {
            try {
                // Check if the selected run is in the live builds
                const response = await fetch('/api/live');
                const liveBuilds = await response.json();
                const isLive = liveBuilds && liveBuilds.some(build => build.id === selectedRunId);
                
                if (isLive) {
                    // Refresh the chart for the live build
                    loadRunDetails(selectedRunId);
                }
            } catch (error) {
                console.error('Error checking if selected run is live:', error);
            }
        }

        function displayRuns(runs) {
            const container = document.getElementById('runs-list');
            container.innerHTML = '';
            
            if (runs.length === 0) {
                container.innerHTML = '<div class="no-runs">No completed builds found</div>';
                return;
            }
            
            runs.forEach(run => {
                const item = document.createElement('div');
                item.className = ` + "`" + `run-item ${run.status} ${selectedRunId === run.id ? 'selected' : ''}` + "`" + `;
                item.onclick = () => selectRun(run.id);
                
                const duration = run.duration ? ` + "`" + `${(run.duration / 1000).toFixed(1)}s` + "`" + ` : 'Running...';
                const statusIcon = {
                    'completed': '✅',
                    'failed': '❌',
                    'running': '🔄',
                    'running_live': '🔴'
                }[run.status] || '⚪';
                
                // Special handling for crashed builds (exit code -1)
                const isCrashed = run.exit_code === -1;
                const displayIcon = isCrashed ? '💥' : statusIcon;
                
                item.innerHTML = ` + "`" + `
                    <div><strong>${displayIcon} ${run.command} ${run.args.join(' ')}</strong></div>
                    <div class="run-meta">
                        ${new Date(run.start_time).toLocaleString()} | 
                        Duration: ${duration} | 
                        Processes: ${run.process_count || 0} |
                        Exit: ${isCrashed ? 'CRASHED' : (run.exit_code !== null ? run.exit_code : 'N/A')}
                    </div>
                ` + "`" + `;
                
                container.appendChild(item);
            });
        }

        function displayLiveBuilds(builds) {
            const container = document.getElementById('live-builds-list');
            container.innerHTML = '';
            
            if (builds.length === 0) {
                container.innerHTML = '<div class="no-runs">No live builds running</div>';
                return;
            }
            
            builds.forEach(build => {
                const item = document.createElement('div');
                item.className = ` + "`" + `run-item running_live ${selectedRunId === build.id ? 'selected' : ''}` + "`" + `;
                item.onclick = () => selectRun(build.id);
                
                const duration = Date.now() - new Date(build.start_time).getTime();
                const durationStr = ` + "`" + `${(duration / 1000).toFixed(1)}s` + "`" + `;
                
                item.innerHTML = ` + "`" + `
                    <div><strong>🔴 ${build.command} ${build.args.join(' ')}</strong></div>
                    <div class="run-meta">
                        ${new Date(build.start_time).toLocaleString()} | 
                        Running for: ${durationStr} | 
                        Processes: ${build.process_count || 0} |
                        Status: LIVE
                    </div>
                ` + "`" + `;
                
                container.appendChild(item);
            });
        }

        function selectRun(runId) {
            selectedRunId = runId;
            
            // Update selected item visual state for both lists
            document.querySelectorAll('.run-item').forEach(item => {
                item.classList.remove('selected');
            });
            event.currentTarget.classList.add('selected');
            
            // Load run details
            loadRunDetails(runId);
            
            // Show details section
            document.getElementById('details-section').style.display = 'block';
            document.getElementById('details-title').textContent = ` + "`" + `📊 Build Details - ${runId.substring(0, 8)}...` + "`" + `;
        }

        async function loadRunDetails(runId) {
            try {
                // Close existing run WebSocket
                if (runWs) {
                    runWs.close();
                }
                
                // Load historical events
                const eventsResponse = await fetch(` + "`" + `/api/runs/${runId}/events` + "`" + `);
                const events = await eventsResponse.json();
                
                // Display timeline
                displayTimeline(events);
                
                // Setup WebSocket for real-time updates (if running)
                setupRunWebSocket(runId);
                
            } catch (error) {
                console.error('Error loading run details:', error);
            }
        }

        function displayTimeline(events) {
            const container = d3.select('#flame-graph');
            container.selectAll('*').remove();
            
            if (events.length === 0) {
                container.append('div')
                    .style('text-align', 'center')
                    .style('padding', '40px')
                    .style('color', '#666')
                    .text('No events found for this build');
                return;
            }
            
            const width = 1200;
            const height = 500;
            const margin = {top: 20, right: 50, bottom: 40, left: 150};
            
            const svg = container.append('svg')
                .attr('width', width)
                .attr('height', height);

            // Process events into timeline data
            const processMap = new Map();
            const startTime = new Date(events[0].timestamp);
            
            events.forEach(event => {
                const pid = event.process.pid;
                if (!processMap.has(pid)) {
                    processMap.set(pid, {
                        pid: pid,
                        name: event.process.name,
                        start: new Date(event.timestamp),
                        end: null,
                        status: 'running'
                    });
                }
                
                const proc = processMap.get(pid);
                if (event.type === 'process_end') {
                    proc.end = new Date(event.timestamp);
                    proc.status = 'completed';
                }
            });
            
            const processes = Array.from(processMap.values())
                .sort((a, b) => a.start - b.start);
            
            if (processes.length === 0) return;
            
            // Calculate time scale
            const endTime = Math.max(...processes.map(p => p.end ? p.end.getTime() : Date.now()));
            const duration = endTime - startTime.getTime();
            
            const xScale = d3.scaleLinear()
                .domain([0, duration])
                .range([margin.left, width - margin.right]);

            const yScale = d3.scaleBand()
                .domain(d3.range(processes.length))
                .range([margin.top, height - margin.bottom])
                .padding(0.1);

            // Draw timeline bars
            const bars = svg.selectAll('.timeline-bar')
                .data(processes)
                .enter()
                .append('g')
                .attr('class', 'timeline-bar');

            bars.append('rect')
                .attr('x', d => xScale(d.start.getTime() - startTime.getTime()))
                .attr('y', (d, i) => yScale(i))
                .attr('width', d => {
                    const end = d.end ? d.end.getTime() : Date.now();
                    return Math.max(2, xScale(end - startTime.getTime()) - xScale(d.start.getTime() - startTime.getTime()));
                })
                .attr('height', yScale.bandwidth())
                .attr('fill', d => d.status === 'running' ? '#4CAF50' : '#FF9800')
                .attr('opacity', 0.8);

            // Add process labels
            bars.append('text')
                .attr('x', margin.left - 5)
                .attr('y', (d, i) => yScale(i) + yScale.bandwidth()/2)
                .attr('text-anchor', 'end')
                .attr('dominant-baseline', 'middle')
                .style('fill', '#ccc')
                .style('font-size', '11px')
                .text(d => ` + "`" + `${d.name} (${d.pid})` + "`" + `);

            // Add time axis
            const timeAxis = d3.axisBottom(xScale)
                .tickFormat(d => ` + "`" + `${Math.round(d/1000)}s` + "`" + `);
                
            svg.append('g')
                .attr('transform', ` + "`" + `translate(0, ${height - margin.bottom})` + "`" + `)
                .style('color', '#ccc')
                .call(timeAxis);
        }

        function setupRunWebSocket(runId) {
            const wsUrl = ` + "`" + `ws://${location.host}/ws/runs/${runId}` + "`" + `;
            runWs = new WebSocket(wsUrl);
            
            runWs.onmessage = function(event) {
                const data = JSON.parse(event.data);
                // Handle real-time updates for this specific run
                console.log('Run update:', data);
            };
            
            runWs.onerror = function(error) {
                console.log('Run WebSocket error:', error);
            };
        }

        function toggleLiveMode() {
            liveMode = !liveMode;
            const btn = document.getElementById('live-btn');
            
            if (liveMode) {
                btn.textContent = 'Exit Live Mode';
                btn.classList.add('active');
                setupLiveWebSocket();
            } else {
                btn.textContent = 'Live Mode';
                btn.classList.remove('active');
                if (liveWs) {
                    liveWs.close();
                    liveWs = null;
                }
            }
        }

        function setupLiveWebSocket() {
            const wsUrl = ` + "`" + `ws://${location.host}/ws/live` + "`" + `;
            liveWs = new WebSocket(wsUrl);
            
            liveWs.onmessage = function(event) {
                const data = JSON.parse(event.data);
                console.log('Live update:', data);
                
                if (data.type === 'initial_state') {
                    // Update runs list with live data
                    displayRuns(data.data);
                } else {
                    // Handle real-time events
                    loadRuns(); // Refresh the runs list
                }
            };
            
            liveWs.onerror = function(error) {
                console.log('Live WebSocket error:', error);
                toggleLiveMode(); // Turn off live mode on error
            };
        }
    </script>
</body>
</html>`;

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// Delete handlers
func (d *BuildspyDaemon) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters for bulk delete
	query := r.URL.Query()
	runIDs := query["run_id"]
	
	if len(runIDs) == 0 {
		http.Error(w, "No run IDs provided", http.StatusBadRequest)
		return
	}
	
	var deletedCount int
	var errors []string
	
	for _, runID := range runIDs {
		// Check if run is currently live
		if _, isLive := d.liveBuilds[runID]; isLive {
			errors = append(errors, fmt.Sprintf("Cannot delete live build: %s", runID))
			continue
		}
		
		if err := d.database.DeleteBuildRun(runID); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to delete %s: %v", runID, err))
		} else {
			deletedCount++
		}
	}
	
	response := map[string]interface{}{
		"deleted_count": deletedCount,
		"total_requested": len(runIDs),
	}
	
	if len(errors) > 0 {
		response["errors"] = errors
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (d *BuildspyDaemon) handleDeleteSpecificRun(w http.ResponseWriter, r *http.Request, runID string) {
	// Check if run is currently live
	if _, isLive := d.liveBuilds[runID]; isLive {
		http.Error(w, "Cannot delete live build", http.StatusBadRequest)
		return
	}
	
	if err := d.database.DeleteBuildRun(runID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "Build run not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func (d *BuildspyDaemon) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var request struct {
		RunIDs []string `json:"run_ids"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	
	if len(request.RunIDs) == 0 {
		http.Error(w, "No run IDs provided", http.StatusBadRequest)
		return
	}
	
	var deletedCount int
	var errors []string
	
	for _, runID := range request.RunIDs {
		// Check if run is currently live
		if _, isLive := d.liveBuilds[runID]; isLive {
			errors = append(errors, fmt.Sprintf("Cannot delete live build: %s", runID))
			continue
		}
		
		if err := d.database.DeleteBuildRun(runID); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to delete %s: %v", runID, err))
		} else {
			deletedCount++
		}
	}
	
	response := map[string]interface{}{
		"deleted_count": deletedCount,
		"total_requested": len(request.RunIDs),
	}
	
	if len(errors) > 0 {
		response["errors"] = errors
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}