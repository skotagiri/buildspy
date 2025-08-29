package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"buildspy/config"
	"buildspy/database"
	"buildspy/models"
	"buildspy/monitoring"
)

func main() {
	var (
		buildCmd = flag.String("cmd", "", "Build command to monitor (e.g., 'make', 'ninja')")
		dataDir  = flag.String("data", config.DefaultDataDir(), "Data directory for storing build metrics")
		verbose  = flag.Bool("verbose", false, "Verbose logging")
	)
	flag.Parse()

	if *buildCmd == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s -cmd \"<build-command>\" [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s -cmd \"make -j16\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -cmd \"ninja -v\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -cmd \"cmake --build . --parallel 8\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Parse command and arguments
	cmdParts := strings.Fields(*buildCmd)
	if len(cmdParts) == 0 {
		log.Fatalf("Invalid build command: %s", *buildCmd)
	}

	// Get current working directory
	workingDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}

	cfg := &config.CLIConfig{
		Command:    cmdParts[0],
		Args:       append(cmdParts[1:], flag.Args()...),
		DataDir:    *dataDir,
		Verbose:    *verbose,
		WorkingDir: workingDir,
	}

	// Run the build and monitor it
	if err := runBuildAndMonitor(cfg); err != nil {
		log.Fatalf("Build monitoring failed: %v", err)
	}
}

func runBuildAndMonitor(cfg *config.CLIConfig) error {
	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Initialize database
	db, err := database.NewDatabase(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// Create build run
	buildRun := models.NewBuildRun(cfg.Command, cfg.Args, cfg.WorkingDir)
	
	// Save initial build run to database
	if err := db.SaveBuildRun(buildRun); err != nil {
		return fmt.Errorf("failed to save build run: %w", err)
	}

	// Create process monitor
	monitor := monitoring.NewProcessMonitor(db, buildRun, cfg.Verbose, nil)
	defer monitor.Stop()

	// Start the build command
	cmd, err := monitor.StartCommand(cfg.Command, cfg.Args, cfg.WorkingDir)
	if err != nil {
		return err
	}

	// Connect stdout/stderr to current process for user visibility
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start monitoring in background
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	monitorDone := make(chan struct{})
	go func() {
		monitor.MonitorProcessTree(int32(cmd.Process.Pid))
		close(monitorDone)
	}()

	// Wait for the build to complete
	err = cmd.Wait()
	
	// Mark build as completed
	exitCode := 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}
	
	buildRun.Complete(exitCode)
	buildRun.ProcessCount = monitor.GetProcessCount()
	
	// Save updated build run
	if saveErr := db.SaveBuildRun(buildRun); saveErr != nil {
		if cfg.Verbose {
			fmt.Printf("Warning: failed to update build run: %v\n", saveErr)
		}
	}

	// Create build completion event
	completionEvent := models.NewBuildEvent(buildRun.ID, "build_complete", models.ProcessInfo{
		BuildRunID: buildRun.ID,
		PID:        int32(cmd.Process.Pid),
		Name:       "build",
		Status:     buildRun.Status,
		StartTime:  buildRun.StartTime,
	})
	
	if saveErr := db.SaveBuildEvent(completionEvent); saveErr != nil {
		if cfg.Verbose {
			fmt.Printf("Warning: failed to save completion event: %v\n", saveErr)
		}
	}

	// Stop monitoring
	cancel()
	
	// Wait for monitoring to finish
	select {
	case <-monitorDone:
	case <-time.After(5 * time.Second):
		if cfg.Verbose {
			fmt.Println("Warning: monitoring cleanup timed out")
		}
	}

	if cfg.Verbose {
		if err != nil {
			fmt.Printf("Build completed with error (exit code %d): %v\n", exitCode, err)
		} else {
			fmt.Printf("Build completed successfully\n")
		}
		fmt.Printf("Total processes monitored: %d\n", monitor.GetProcessCount())
		fmt.Printf("Build monitoring completed. Data stored in: %s\n", cfg.DataDir)
		fmt.Printf("Run ID: %s\n", buildRun.ID)
	}

	return err // Return the original build error if any
}