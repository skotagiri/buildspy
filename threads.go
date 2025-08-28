package main

import (
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
	"sync"
)

// ThreadInfo contains information about a thread
type ThreadInfo struct {
	TID       int32   `json:"tid"`
	PID       int32   `json:"pid"`
	Name      string  `json:"name"`
	State     string  `json:"state"`
	CPUTime   uint64  `json:"cpu_time"`
	Priority  int     `json:"priority"`
	Nice      int     `json:"nice"`
	VolCtx    uint64  `json:"voluntary_context_switches"`
	InvolCtx  uint64  `json:"involuntary_context_switches"`
}

// getThreadInfo reads thread information from /proc/[pid]/task/[tid]/
func getThreadInfo(pid, tid int32) (*ThreadInfo, error) {
	// Read thread status from /proc/[pid]/task/[tid]/stat
	statPath := fmt.Sprintf("/proc/%d/task/%d/stat", pid, tid)
	statData, err := ioutil.ReadFile(statPath)
	if err != nil {
		return nil, err
	}
	
	// Parse stat file - format is complex but we need specific fields
	fields := strings.Fields(string(statData))
	if len(fields) < 44 {
		return nil, fmt.Errorf("invalid stat format")
	}
	
	threadInfo := &ThreadInfo{
		TID: tid,
		PID: pid,
	}
	
	// Field 3: state
	threadInfo.State = fields[2]
	
	// Fields 14,15: utime, stime (CPU time in clock ticks)
	if utime, err := strconv.ParseUint(fields[13], 10, 64); err == nil {
		if stime, err := strconv.ParseUint(fields[14], 10, 64); err == nil {
			threadInfo.CPUTime = utime + stime
		}
	}
	
	// Field 18: priority
	if priority, err := strconv.Atoi(fields[17]); err == nil {
		threadInfo.Priority = priority
	}
	
	// Field 19: nice value
	if nice, err := strconv.Atoi(fields[18]); err == nil {
		threadInfo.Nice = nice
	}
	
	// Read thread name from comm file
	commPath := fmt.Sprintf("/proc/%d/task/%d/comm", pid, tid)
	if commData, err := ioutil.ReadFile(commPath); err == nil {
		threadInfo.Name = strings.TrimSpace(string(commData))
	}
	
	// Read context switches from status file
	statusPath := fmt.Sprintf("/proc/%d/task/%d/status", pid, tid)
	if statusData, err := ioutil.ReadFile(statusPath); err == nil {
		lines := strings.Split(string(statusData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "voluntary_ctxt_switches:") {
				if val, err := strconv.ParseUint(strings.Fields(line)[1], 10, 64); err == nil {
					threadInfo.VolCtx = val
				}
			} else if strings.HasPrefix(line, "nonvoluntary_ctxt_switches:") {
				if val, err := strconv.ParseUint(strings.Fields(line)[1], 10, 64); err == nil {
					threadInfo.InvolCtx = val
				}
			}
		}
	}
	
	return threadInfo, nil
}

// getProcessThreads returns all threads for a given process
func getProcessThreads(pid int32) ([]*ThreadInfo, error) {
	taskDir := fmt.Sprintf("/proc/%d/task", pid)
	
	// Read all thread directories
	taskEntries, err := ioutil.ReadDir(taskDir)
	if err != nil {
		return nil, err
	}
	
	var threads []*ThreadInfo
	for _, entry := range taskEntries {
		if !entry.IsDir() {
			continue
		}
		
		// Parse thread ID
		tid, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil {
			continue
		}
		
		// Get thread information
		threadInfo, err := getThreadInfo(pid, int32(tid))
		if err != nil {
			continue // Thread might have disappeared
		}
		
		threads = append(threads, threadInfo)
	}
	
	return threads, nil
}

// Enhanced ProcessInfo with thread information
type ProcessInfoWithThreads struct {
	ProcessInfo
	Threads []ThreadInfo `json:"threads"`
}

// updateProcessThreads adds thread monitoring to the build monitor
func (bm *BuildMonitor) updateProcessThreads(pid int32) {
	threads, err := getProcessThreads(pid)
	if err != nil {
		if bm.verbose {
			fmt.Printf("Failed to get threads for PID %d: %v\n", pid, err)
		}
		return
	}
	
	// Store thread information (could extend ProcessInfo or create separate storage)
	if bm.verbose && len(threads) > 1 {
		fmt.Printf("PID %d has %d threads\n", pid, len(threads))
		for _, thread := range threads {
			if thread.TID != pid { // Don't log main thread separately
				fmt.Printf("  Thread %d: %s (state: %s, ctx_switches: %d/%d)\n", 
					thread.TID, thread.Name, thread.State, thread.VolCtx, thread.InvolCtx)
			}
		}
	}
}

// ThreadMonitor provides detailed thread tracking capabilities
type ThreadMonitor struct {
	processThreads map[int32][]*ThreadInfo // PID -> thread list
	threadHistory  map[int32][]ThreadInfo  // TID -> historical data
	mutex          sync.RWMutex
}

// NewThreadMonitor creates a new thread monitoring instance
func NewThreadMonitor() *ThreadMonitor {
	return &ThreadMonitor{
		processThreads: make(map[int32][]*ThreadInfo),
		threadHistory:  make(map[int32][]ThreadInfo),
	}
}

// UpdateThreads updates thread information for all monitored processes
func (tm *ThreadMonitor) UpdateThreads(pids []int32) {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()
	
	for _, pid := range pids {
		threads, err := getProcessThreads(pid)
		if err != nil {
			continue
		}
		
		// Store current thread state
		tm.processThreads[pid] = threads
		
		// Update thread history for analysis
		for _, thread := range threads {
			if tm.threadHistory[thread.TID] == nil {
				tm.threadHistory[thread.TID] = make([]ThreadInfo, 0)
			}
			tm.threadHistory[thread.TID] = append(tm.threadHistory[thread.TID], *thread)
			
			// Keep only last 100 entries per thread
			if len(tm.threadHistory[thread.TID]) > 100 {
				tm.threadHistory[thread.TID] = tm.threadHistory[thread.TID][1:]
			}
		}
	}
}

// GetThreadInfo returns current thread information for a process
func (tm *ThreadMonitor) GetThreadInfo(pid int32) ([]*ThreadInfo, bool) {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()
	
	threads, exists := tm.processThreads[pid]
	return threads, exists
}

// GetThreadHistory returns historical data for a thread
func (tm *ThreadMonitor) GetThreadHistory(tid int32) ([]ThreadInfo, bool) {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()
	
	history, exists := tm.threadHistory[tid]
	return history, exists
}