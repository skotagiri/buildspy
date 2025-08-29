package config

import (
	"os"
	"path/filepath"
)

// DefaultDataDir returns the default data directory for BuildSpy
func DefaultDataDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".buildspy"
	}
	return filepath.Join(homeDir, ".buildspy")
}

// CLIConfig contains configuration for the buildspy CLI tool
type CLIConfig struct {
	Command    string
	Args       []string
	DataDir    string
	Verbose    bool
	WorkingDir string
}

// DaemonConfig contains configuration for the buildspyd daemon
type DaemonConfig struct {
	DataDir string
	Port    int
	Verbose bool
}