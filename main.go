package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	var (
		buildCmd = flag.String("cmd", "", "Build command to monitor (e.g., 'make', 'ninja')")
		webPort  = flag.Int("port", 8080, "Web server port for visualization")
		verbose  = flag.Bool("v", false, "Verbose logging")
	)
	flag.Parse()

	if *buildCmd == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s -cmd <build-command> [options]\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Initialize the monitoring system
	monitor := NewBuildMonitor(*verbose)
	
	// Start web server for real-time visualization
	go monitor.StartWebServer(*webPort)
	
	// Monitor the build process
	if err := monitor.MonitorBuild(*buildCmd, flag.Args()...); err != nil {
		log.Fatalf("Monitoring failed: %v", err)
	}
	
	// Wait for interrupt signal to keep server running after build completes
	monitor.WaitForExit()
}