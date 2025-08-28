package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

func main() {
	var (
		buildCmd = flag.String("cmd", "", "Build command to monitor (e.g., 'make', 'ninja')")
		webPort  = flag.Int("port", 8080, "Web server port for visualization")
		verbose  = flag.Bool("v", false, "Verbose logging")
	)
	flag.Parse()

	if *buildCmd == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s -cmd \"<build-command>\" [options]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s -cmd \"make -j16\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -cmd \"ninja -v\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -cmd \"cmake --build . --parallel 8\"\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Initialize the monitoring system
	monitor := NewBuildMonitor(*verbose)
	
	// Start web server for real-time visualization
	go monitor.StartWebServer(*webPort)
	
	// Parse command and arguments
	cmdParts := strings.Fields(*buildCmd)
	if len(cmdParts) == 0 {
		log.Fatalf("Invalid build command: %s", *buildCmd)
	}
	
	command := cmdParts[0]
	args := append(cmdParts[1:], flag.Args()...)
	
	// Monitor the build process
	if err := monitor.MonitorBuild(command, args...); err != nil {
		log.Fatalf("Monitoring failed: %v", err)
	}
	
	// Wait for interrupt signal to keep server running after build completes
	monitor.WaitForExit()
}