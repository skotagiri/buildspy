package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildSystemType represents different build systems
type BuildSystemType int

const (
	BuildSystemUnknown BuildSystemType = iota
	BuildSystemMake
	BuildSystemNinja
	BuildSystemCMake
	BuildSystemBazel
	BuildSystemGradleJava
	BuildSystemCargo
	BuildSystemNpm
)

// BuildSystemInfo contains information about the detected build system
type BuildSystemInfo struct {
	Type        BuildSystemType `json:"type"`
	Name        string          `json:"name"`
	ConfigFile  string          `json:"config_file"`
	Command     string          `json:"command"`
	WorkingDir  string          `json:"working_dir"`
	Parallelism int             `json:"parallelism"`
}

// DetectBuildSystem analyzes the current directory to identify the build system
func DetectBuildSystem(workDir string) *BuildSystemInfo {
	// Check for various build system indicators
	buildSystems := []struct {
		files     []string
		buildType BuildSystemType
		name      string
	}{
		{[]string{"Makefile", "makefile", "GNUmakefile"}, BuildSystemMake, "Make"},
		{[]string{"build.ninja"}, BuildSystemNinja, "Ninja"},
		{[]string{"CMakeLists.txt"}, BuildSystemCMake, "CMake"},
		{[]string{"BUILD", "BUILD.bazel", "WORKSPACE"}, BuildSystemBazel, "Bazel"},
		{[]string{"build.gradle", "build.gradle.kts"}, BuildSystemGradleJava, "Gradle"},
		{[]string{"Cargo.toml"}, BuildSystemCargo, "Cargo"},
		{[]string{"package.json"}, BuildSystemNpm, "npm"},
	}
	
	for _, system := range buildSystems {
		for _, filename := range system.files {
			configPath := filepath.Join(workDir, filename)
			if _, err := os.Stat(configPath); err == nil {
				return &BuildSystemInfo{
					Type:       system.buildType,
					Name:       system.name,
					ConfigFile: configPath,
					Command:    getDefaultCommand(system.buildType),
					WorkingDir: workDir,
					Parallelism: getDefaultParallelism(system.buildType),
				}
			}
		}
	}
	
	return &BuildSystemInfo{
		Type: BuildSystemUnknown,
		Name: "Unknown",
		WorkingDir: workDir,
	}
}

// getDefaultCommand returns the typical build command for each build system
func getDefaultCommand(buildType BuildSystemType) string {
	switch buildType {
	case BuildSystemMake:
		return "make"
	case BuildSystemNinja:
		return "ninja"
	case BuildSystemCMake:
		return "cmake --build ."
	case BuildSystemBazel:
		return "bazel build //..."
	case BuildSystemGradleJava:
		return "./gradlew build"
	case BuildSystemCargo:
		return "cargo build"
	case BuildSystemNpm:
		return "npm run build"
	default:
		return ""
	}
}

// getDefaultParallelism returns typical parallelism settings for build systems
func getDefaultParallelism(buildType BuildSystemType) int {
	switch buildType {
	case BuildSystemMake:
		return 4 // make -j4
	case BuildSystemNinja:
		return 0 // ninja auto-detects
	case BuildSystemBazel:
		return 0 // bazel auto-manages
	case BuildSystemCargo:
		return 0 // cargo auto-detects
	default:
		return 1
	}
}

// AnalyzeBuildPattern analyzes build patterns and suggests optimizations
func (bsi *BuildSystemInfo) AnalyzeBuildPattern(events []BuildEvent) *BuildAnalysis {
	analysis := &BuildAnalysis{
		BuildSystem:     bsi.Name,
		TotalProcesses:  len(events),
		ParallelismUsed: calculateActualParallelism(events),
		Bottlenecks:     findBottlenecks(events),
		Suggestions:     generateSuggestions(bsi, events),
	}
	
	return analysis
}

// BuildAnalysis contains analysis results of a build
type BuildAnalysis struct {
	BuildSystem     string             `json:"build_system"`
	TotalProcesses  int                `json:"total_processes"`
	ParallelismUsed int                `json:"parallelism_used"`
	Bottlenecks     []ProcessBottleneck `json:"bottlenecks"`
	Suggestions     []string           `json:"suggestions"`
	Duration        float64            `json:"duration_seconds"`
}

// ProcessBottleneck identifies processes that are limiting build performance
type ProcessBottleneck struct {
	ProcessName string  `json:"process_name"`
	PID         int32   `json:"pid"`
	Duration    float64 `json:"duration_seconds"`
	CPUUsage    float64 `json:"avg_cpu_usage"`
	Reason      string  `json:"reason"`
}

// calculateActualParallelism determines how many processes ran concurrently
func calculateActualParallelism(events []BuildEvent) int {
	if len(events) == 0 {
		return 0
	}
	
	// Track concurrent processes over time
	maxConcurrent := 0
	currentConcurrent := 0
	
	// Sort events by timestamp (simplified - assumes they're already ordered)
	for _, event := range events {
		switch event.Type {
		case "process_start":
			currentConcurrent++
			if currentConcurrent > maxConcurrent {
				maxConcurrent = currentConcurrent
			}
		case "process_end":
			currentConcurrent--
		}
	}
	
	return maxConcurrent
}

// findBottlenecks identifies processes that are limiting performance
func findBottlenecks(events []BuildEvent) []ProcessBottleneck {
	processData := make(map[int32]*ProcessBottleneck)
	
	for _, event := range events {
		pid := event.Process.PID
		if processData[pid] == nil {
			processData[pid] = &ProcessBottleneck{
				ProcessName: event.Process.Name,
				PID:         pid,
			}
		}
		
		bottleneck := processData[pid]
		
		// Calculate duration and resource usage
		if event.Type == "process_end" && event.Process.EndTime != nil {
			duration := event.Process.EndTime.Sub(event.Process.StartTime).Seconds()
			bottleneck.Duration = duration
		}
		
		if event.Process.CPUUsage > bottleneck.CPUUsage {
			bottleneck.CPUUsage = event.Process.CPUUsage
		}
	}
	
	// Identify bottlenecks (processes taking >10% of total time or using <50% CPU)
	var bottlenecks []ProcessBottleneck
	for _, bottleneck := range processData {
		reason := ""
		if bottleneck.Duration > 10 { // Long-running process
			reason = "Long execution time"
		} else if bottleneck.CPUUsage < 50 { // Low CPU usage might indicate I/O bound
			reason = "Potentially I/O bound"
		}
		
		if reason != "" {
			bottleneck.Reason = reason
			bottlenecks = append(bottlenecks, *bottleneck)
		}
	}
	
	return bottlenecks
}

// generateSuggestions provides optimization recommendations based on build analysis
func generateSuggestions(bsi *BuildSystemInfo, events []BuildEvent) []string {
	var suggestions []string
	
	// Analyze parallelism
	actualParallelism := calculateActualParallelism(events)
	if actualParallelism < 2 && bsi.Type == BuildSystemMake {
		suggestions = append(suggestions, 
			fmt.Sprintf("Consider using 'make -j%d' to enable parallel builds", 
				min(4, getNumCPUs())))
	}
	
	// Check for I/O bottlenecks
	bottlenecks := findBottlenecks(events)
	ioBoundCount := 0
	for _, bottleneck := range bottlenecks {
		if strings.Contains(bottleneck.Reason, "I/O") {
			ioBoundCount++
		}
	}
	
	if ioBoundCount > 0 {
		suggestions = append(suggestions, 
			"Consider using faster storage (SSD) or tmpfs for build artifacts")
	}
	
	// Build system specific suggestions
	switch bsi.Type {
	case BuildSystemMake:
		suggestions = append(suggestions, 
			"Consider migrating to Ninja for faster incremental builds")
	case BuildSystemNinja:
		suggestions = append(suggestions, 
			"Ninja is already optimized - focus on compilation flags and dependencies")
	}
	
	return suggestions
}

// getNumCPUs returns the number of CPU cores (simplified implementation)
func getNumCPUs() int {
	// In a real implementation, use runtime.NumCPU() or similar
	return 4
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}