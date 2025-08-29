package models

import (
	"time"

	"github.com/google/uuid"
)

// BuildRun represents a single build execution session
type BuildRun struct {
	ID           string     `json:"id"`                  // UUID for the build run
	Command      string     `json:"command"`             // Build command executed
	Args         []string   `json:"args"`                // Command arguments
	StartTime    time.Time  `json:"start_time"`          // When build started
	EndTime      *time.Time `json:"end_time,omitempty"`  // When build completed (nil if running)
	Status       string     `json:"status"`              // "running", "completed", "failed"
	ExitCode     *int       `json:"exit_code,omitempty"` // Exit code if completed
	WorkingDir   string     `json:"working_dir"`         // Directory where build was executed
	ProcessCount int        `json:"process_count"`       // Total processes spawned
	Duration     *int64     `json:"duration,omitempty"`  // Duration in milliseconds (nil if running)
}

// NewBuildRun creates a new build run with generated UUID
func NewBuildRun(command string, args []string, workingDir string) *BuildRun {
	return &BuildRun{
		ID:         uuid.New().String(),
		Command:    command,
		Args:       args,
		StartTime:  time.Now(),
		Status:     "running",
		WorkingDir: workingDir,
	}
}

// Complete marks the build run as completed
func (br *BuildRun) Complete(exitCode int) {
	now := time.Now()
	br.EndTime = &now
	br.ExitCode = &exitCode
	if exitCode == 0 {
		br.Status = "completed"
	} else {
		br.Status = "failed"
	}
	duration := now.Sub(br.StartTime).Milliseconds()
	br.Duration = &duration
}

// ProcessInfo contains detailed information about a monitored process
// Extended from original with build run association
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

// BuildEvent represents a timeline event during the build
// Extended from original with build run association
type BuildEvent struct {
	ID         string      `json:"id"`           // UUID for the event
	BuildRunID string      `json:"build_run_id"` // Associated build run
	Timestamp  time.Time   `json:"timestamp"`
	Type       string      `json:"type"` // "process_start", "process_end", "resource_update", "build_complete"
	Process    ProcessInfo `json:"process"`
}

// NewBuildEvent creates a new build event with generated UUID
func NewBuildEvent(buildRunID string, eventType string, process ProcessInfo) *BuildEvent {
	return &BuildEvent{
		ID:         uuid.New().String(),
		BuildRunID: buildRunID,
		Timestamp:  time.Now(),
		Type:       eventType,
		Process:    process,
	}
}

// Database key constants for BadgerDB storage
const (
	KeyPrefixBuildRun  = "buildrun:"
	KeyPrefixEvent     = "event:"
	KeyPrefixProcess   = "process:"
	KeyPrefixRunEvents = "runevents:" // Index: buildrun_id -> list of event IDs
	KeyPrefixRunProcs  = "runprocs:"  // Index: buildrun_id -> list of process PIDs
	KeyTimeIndex       = "timeindex:" // Time-based index for efficient querying
)

// Key GenerateKey creates database keys for different entity types
func (br *BuildRun) Key() string {
	return KeyPrefixBuildRun + br.ID
}

func (be *BuildEvent) Key() string {
	return KeyPrefixEvent + be.ID
}

func (pi *ProcessInfo) Key() string {
	return KeyPrefixProcess + pi.BuildRunID + ":" + string(rune(pi.PID))
}

// Index keys for efficient querying
func RunEventsKey(buildRunID string) string {
	return KeyPrefixRunEvents + buildRunID
}

func RunProcessesKey(buildRunID string) string {
	return KeyPrefixRunProcs + buildRunID
}

func TimeIndexKey(timestamp time.Time) string {
	return KeyTimeIndex + timestamp.Format(time.RFC3339Nano)
}
