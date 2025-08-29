package database

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"buildspy/models"
)

// Database wraps BadgerDB and provides build-specific operations
type Database struct {
	db   *badger.DB
	path string
}

// NewDatabase creates a new database instance
func NewDatabase(dataDir string) (*Database, error) {
	dbPath := filepath.Join(dataDir, "buildspy.db")
	
	opts := badger.DefaultOptions(dbPath)
	opts.Logger = nil // Disable badger logging
	
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return &Database{
		db:   db,
		path: dbPath,
	}, nil
}

// Close closes the database connection
func (d *Database) Close() error {
	return d.db.Close()
}

// BuildRun operations
func (d *Database) SaveBuildRun(buildRun *models.BuildRun) error {
	return d.db.Update(func(txn *badger.Txn) error {
		data, err := json.Marshal(buildRun)
		if err != nil {
			return err
		}
		return txn.Set([]byte(buildRun.Key()), data)
	})
}

func (d *Database) GetBuildRun(id string) (*models.BuildRun, error) {
	var buildRun models.BuildRun
	err := d.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(models.KeyPrefixBuildRun + id))
		if err != nil {
			return err
		}
		
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &buildRun)
		})
	})
	
	if err != nil {
		return nil, err
	}
	return &buildRun, nil
}

func (d *Database) ListBuildRuns() ([]*models.BuildRun, error) {
	var buildRuns []*models.BuildRun
	
	err := d.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(models.KeyPrefixBuildRun)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			err := item.Value(func(val []byte) error {
				var buildRun models.BuildRun
				if err := json.Unmarshal(val, &buildRun); err != nil {
					return err
				}
				buildRuns = append(buildRuns, &buildRun)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Sort by start time, newest first
	sort.Slice(buildRuns, func(i, j int) bool {
		return buildRuns[i].StartTime.After(buildRuns[j].StartTime)
	})

	return buildRuns, nil
}

// BuildEvent operations
func (d *Database) SaveBuildEvent(event *models.BuildEvent) error {
	return d.db.Update(func(txn *badger.Txn) error {
		// Save the event
		eventData, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := txn.Set([]byte(event.Key()), eventData); err != nil {
			return err
		}

		// Update the run events index
		runEventsKey := models.RunEventsKey(event.BuildRunID)
		var eventIDs []string
		
		// Get existing event IDs for this run
		item, err := txn.Get([]byte(runEventsKey))
		if err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		
		if err == nil {
			err = item.Value(func(val []byte) error {
				return json.Unmarshal(val, &eventIDs)
			})
			if err != nil {
				return err
			}
		}

		// Add new event ID
		eventIDs = append(eventIDs, event.ID)
		indexData, err := json.Marshal(eventIDs)
		if err != nil {
			return err
		}

		return txn.Set([]byte(runEventsKey), indexData)
	})
}

func (d *Database) GetBuildEvents(buildRunID string) ([]*models.BuildEvent, error) {
	var events []*models.BuildEvent
	
	err := d.db.View(func(txn *badger.Txn) error {
		// Get event IDs for this run
		runEventsKey := models.RunEventsKey(buildRunID)
		item, err := txn.Get([]byte(runEventsKey))
		if err == badger.ErrKeyNotFound {
			return nil // No events found
		}
		if err != nil {
			return err
		}

		var eventIDs []string
		err = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &eventIDs)
		})
		if err != nil {
			return err
		}

		// Fetch each event
		for _, eventID := range eventIDs {
			eventItem, err := txn.Get([]byte(models.KeyPrefixEvent + eventID))
			if err != nil {
				continue // Skip missing events
			}

			err = eventItem.Value(func(val []byte) error {
				var event models.BuildEvent
				if err := json.Unmarshal(val, &event); err != nil {
					return err
				}
				events = append(events, &event)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Sort by timestamp
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	return events, nil
}

// ProcessInfo operations
func (d *Database) SaveProcess(process *models.ProcessInfo) error {
	return d.db.Update(func(txn *badger.Txn) error {
		// Save the process
		processData, err := json.Marshal(process)
		if err != nil {
			return err
		}
		if err := txn.Set([]byte(process.Key()), processData); err != nil {
			return err
		}

		// Update the run processes index
		runProcsKey := models.RunProcessesKey(process.BuildRunID)
		var processPIDs []int32
		
		// Get existing process PIDs for this run
		item, err := txn.Get([]byte(runProcsKey))
		if err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		
		if err == nil {
			err = item.Value(func(val []byte) error {
				return json.Unmarshal(val, &processPIDs)
			})
			if err != nil {
				return err
			}
		}

		// Add new process PID if not already present
		found := false
		for _, pid := range processPIDs {
			if pid == process.PID {
				found = true
				break
			}
		}
		if !found {
			processPIDs = append(processPIDs, process.PID)
		}

		indexData, err := json.Marshal(processPIDs)
		if err != nil {
			return err
		}

		return txn.Set([]byte(runProcsKey), indexData)
	})
}

func (d *Database) GetProcesses(buildRunID string) ([]*models.ProcessInfo, error) {
	var processes []*models.ProcessInfo
	
	err := d.db.View(func(txn *badger.Txn) error {
		// Get process PIDs for this run
		runProcsKey := models.RunProcessesKey(buildRunID)
		item, err := txn.Get([]byte(runProcsKey))
		if err == badger.ErrKeyNotFound {
			return nil // No processes found
		}
		if err != nil {
			return err
		}

		var processPIDs []int32
		err = item.Value(func(val []byte) error {
			return json.Unmarshal(val, &processPIDs)
		})
		if err != nil {
			return err
		}

		// Fetch each process
		for _, pid := range processPIDs {
			processKey := models.KeyPrefixProcess + buildRunID + ":" + string(rune(pid))
			processItem, err := txn.Get([]byte(processKey))
			if err != nil {
				continue // Skip missing processes
			}

			err = processItem.Value(func(val []byte) error {
				var process models.ProcessInfo
				if err := json.Unmarshal(val, &process); err != nil {
					return err
				}
				processes = append(processes, &process)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Sort by start time
	sort.Slice(processes, func(i, j int) bool {
		return processes[i].StartTime.Before(processes[j].StartTime)
	})

	return processes, nil
}

// Utility operations
func (d *Database) DeleteBuildRun(buildRunID string) error {
	return d.db.Update(func(txn *badger.Txn) error {
		// Delete the build run
		if err := txn.Delete([]byte(models.KeyPrefixBuildRun + buildRunID)); err != nil {
			return err
		}

		// Delete associated events
		runEventsKey := models.RunEventsKey(buildRunID)
		item, err := txn.Get([]byte(runEventsKey))
		if err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		
		if err == nil {
			var eventIDs []string
			err = item.Value(func(val []byte) error {
				return json.Unmarshal(val, &eventIDs)
			})
			if err != nil {
				return err
			}

			// Delete each event
			for _, eventID := range eventIDs {
				txn.Delete([]byte(models.KeyPrefixEvent + eventID))
			}
			
			// Delete the events index
			txn.Delete([]byte(runEventsKey))
		}

		// Delete associated processes
		runProcsKey := models.RunProcessesKey(buildRunID)
		procItem, err := txn.Get([]byte(runProcsKey))
		if err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		
		if err == nil {
			var processPIDs []int32
			err = procItem.Value(func(val []byte) error {
				return json.Unmarshal(val, &processPIDs)
			})
			if err != nil {
				return err
			}

			// Delete each process
			for _, pid := range processPIDs {
				processKey := models.KeyPrefixProcess + buildRunID + ":" + string(rune(pid))
				txn.Delete([]byte(processKey))
			}
			
			// Delete the processes index
			txn.Delete([]byte(runProcsKey))
		}

		return nil
	})
}

// CleanupOldRuns removes build runs older than the specified duration
func (d *Database) CleanupOldRuns(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	
	buildRuns, err := d.ListBuildRuns()
	if err != nil {
		return err
	}

	for _, run := range buildRuns {
		if run.StartTime.Before(cutoff) {
			if err := d.DeleteBuildRun(run.ID); err != nil {
				return fmt.Errorf("failed to delete old run %s: %w", run.ID, err)
			}
		}
	}

	return nil
}

// GetStats returns database statistics
func (d *Database) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})
	
	buildRuns, err := d.ListBuildRuns()
	if err != nil {
		return nil, err
	}
	
	stats["total_runs"] = len(buildRuns)
	
	if len(buildRuns) > 0 {
		stats["oldest_run"] = buildRuns[len(buildRuns)-1].StartTime
		stats["newest_run"] = buildRuns[0].StartTime
		
		// Count by status
		statusCount := make(map[string]int)
		for _, run := range buildRuns {
			statusCount[run.Status]++
		}
		stats["by_status"] = statusCount
	}

	return stats, nil
}

// Search functions
func (d *Database) SearchBuildRuns(query string) ([]*models.BuildRun, error) {
	allRuns, err := d.ListBuildRuns()
	if err != nil {
		return nil, err
	}

	if query == "" {
		return allRuns, nil
	}

	var matches []*models.BuildRun
	query = strings.ToLower(query)
	
	for _, run := range allRuns {
		// Search in command, args, and working directory
		searchText := strings.ToLower(run.Command + " " + strings.Join(run.Args, " ") + " " + run.WorkingDir)
		if strings.Contains(searchText, query) {
			matches = append(matches, run)
		}
	}

	return matches, nil
}