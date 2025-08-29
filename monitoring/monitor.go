package monitoring

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"buildspy/database"
	"buildspy/models"
)

// ProcessMonitor handles the core process monitoring logic
type ProcessMonitor struct {
	database     *database.Database
	buildRun     *models.BuildRun
	processes    map[int32]*models.ProcessInfo
	mutex        sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	verbose      bool
	eventHandler EventHandler
}

// EventHandler is called when build events occur
type EventHandler func(event *models.BuildEvent)

// NewProcessMonitor creates a new process monitor instance
func NewProcessMonitor(db *database.Database, buildRun *models.BuildRun, verbose bool, handler EventHandler) *ProcessMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &ProcessMonitor{
		database:     db,
		buildRun:     buildRun,
		processes:    make(map[int32]*models.ProcessInfo),
		ctx:          ctx,
		cancel:       cancel,
		verbose:      verbose,
		eventHandler: handler,
	}
}

// StartCommand starts a command and returns its PID for monitoring
func (pm *ProcessMonitor) StartCommand(command string, args []string, workingDir string) (*exec.Cmd, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = workingDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group to track all children
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	if pm.verbose {
		fmt.Printf("Started build process: %s (PID: %d)\n", command, cmd.Process.Pid)
	}

	return cmd, nil
}

// MonitorProcessTree starts monitoring all processes in the tree starting from rootPID
func (pm *ProcessMonitor) MonitorProcessTree(rootPID int32) {
	ticker := time.NewTicker(50 * time.Millisecond) // High frequency monitoring
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			pm.updateProcessTree(rootPID)
		}
	}
}

// Stop stops the monitoring
func (pm *ProcessMonitor) Stop() {
	pm.cancel()
}

// updateProcessTree scans and updates information about all processes in the tree
func (pm *ProcessMonitor) updateProcessTree(rootPID int32) {
	// Get all processes to find children
	allProcs, err := process.Processes()
	if err != nil {
		return
	}

	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	// Track active PIDs in this scan
	activePIDs := make(map[int32]bool)

	// Find all processes that are part of our build tree
	for _, proc := range allProcs {
		pid := proc.Pid

		// Check if this process is our root or a descendant
		if pid == rootPID || pm.isDescendant(pid, rootPID) {
			activePIDs[pid] = true

			// Get or create process info
			if _, exists := pm.processes[pid]; !exists {
				procInfo := &models.ProcessInfo{
					BuildRunID: pm.buildRun.ID,
					PID:        pid,
					StartTime:  time.Now(),
					Status:     "running",
				}
				pm.processes[pid] = procInfo

				// Save to database (if not in daemon mode)
				if pm.database != nil {
					if err := pm.database.SaveProcess(procInfo); err != nil && pm.verbose {
						fmt.Printf("Warning: failed to save process %d: %v\n", pid, err)
					}
				}

				// Create process start event
				startEvent := models.NewBuildEvent(pm.buildRun.ID, "process_start", *procInfo)
				if pm.database != nil {
					if err := pm.database.SaveBuildEvent(startEvent); err != nil && pm.verbose {
						fmt.Printf("Warning: failed to save start event for process %d: %v\n", pid, err)
					}
				}

				// Notify event handler
				if pm.eventHandler != nil {
					pm.eventHandler(startEvent)
				}

				if pm.verbose {
					fmt.Printf("New process detected: PID %d\n", pid)
				}
			}

			// Update process information
			pm.updateProcessInfo(proc)
		}
	}

	// Mark ended processes
	for pid, procInfo := range pm.processes {
		if !activePIDs[pid] && procInfo.EndTime == nil {
			now := time.Now()
			procInfo.EndTime = &now
			procInfo.Status = "completed"

			// Save updated process (if not in daemon mode)
			if pm.database != nil {
				if err := pm.database.SaveProcess(procInfo); err != nil && pm.verbose {
					fmt.Printf("Warning: failed to update ended process %d: %v\n", pid, err)
				}
			}

			// Create process end event
			endEvent := models.NewBuildEvent(pm.buildRun.ID, "process_end", *procInfo)
			if pm.database != nil {
				if err := pm.database.SaveBuildEvent(endEvent); err != nil && pm.verbose {
					fmt.Printf("Warning: failed to save end event for process %d: %v\n", pid, err)
				}
			}

			// Notify event handler
			if pm.eventHandler != nil {
				pm.eventHandler(endEvent)
			}

			if pm.verbose {
				fmt.Printf("Process ended: PID %d (%s)\n", pid, procInfo.Name)
			}
		}
	}
}

// isDescendant checks if pid is a descendant of rootPID
func (pm *ProcessMonitor) isDescendant(pid, rootPID int32) bool {
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

	// Recursively check parent chain (with depth limit to prevent infinite loops)
	return pm.isDescendantWithDepth(ppid, rootPID, 10)
}

func (pm *ProcessMonitor) isDescendantWithDepth(pid, rootPID int32, depth int) bool {
	if depth <= 0 || pid == rootPID {
		return pid == rootPID
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

	return pm.isDescendantWithDepth(ppid, rootPID, depth-1)
}

// updateProcessInfo updates detailed information for a process
func (pm *ProcessMonitor) updateProcessInfo(proc *process.Process) {
	pid := proc.Pid
	procInfo := pm.processes[pid]

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

	// Save resource update events only for significant activity
	if procInfo.CPUUsage > 5.0 || len(procInfo.Children) > 0 {
		resourceEvent := models.NewBuildEvent(pm.buildRun.ID, "resource_update", *procInfo)
		if pm.database != nil {
			if err := pm.database.SaveBuildEvent(resourceEvent); err != nil && pm.verbose {
				fmt.Printf("Warning: failed to save resource event for process %d: %v\n", pid, err)
			}
		}

		// Notify event handler
		if pm.eventHandler != nil {
			pm.eventHandler(resourceEvent)
		}
	}
}

// GetProcessCount returns the current number of monitored processes
func (pm *ProcessMonitor) GetProcessCount() int {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()
	return len(pm.processes)
}