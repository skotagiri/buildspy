package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"buildspy/models"
)

// APIClient handles communication with the BuildSpy daemon
type APIClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewAPIClient creates a new API client
func NewAPIClient(baseURL string) *APIClient {
	return &APIClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// BuildRunResponse represents the response from build run endpoints
type BuildRunResponse struct {
	Runs      []*models.BuildRun    `json:"runs,omitempty"`
	Events    []*models.BuildEvent  `json:"events,omitempty"`
	Processes []*models.ProcessInfo `json:"processes,omitempty"`
}

// StatsResponse represents the daemon statistics
type StatsResponse struct {
	TotalRuns  int            `json:"total_runs"`
	LiveBuilds int            `json:"live_builds"`
	ByStatus   map[string]int `json:"by_status"`
}

// DeleteResponse represents delete operation response
type DeleteResponse struct {
	DeletedCount   int      `json:"deleted_count"`
	TotalRequested int      `json:"total_requested"`
	Errors         []string `json:"errors,omitempty"`
}

// GetBuildRuns fetches completed build runs
func (c *APIClient) GetBuildRuns(search string, limit int) ([]*models.BuildRun, error) {
	u, err := url.Parse(c.baseURL + "/api/runs")
	if err != nil {
		return nil, err
	}

	params := u.Query()
	if search != "" {
		params.Set("search", search)
	}
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	u.RawQuery = params.Encode()

	resp, err := c.httpClient.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var runs []*models.BuildRun
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, err
	}

	return runs, nil
}

// GetLiveBuilds fetches currently running builds
func (c *APIClient) GetLiveBuilds() ([]*models.BuildRun, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/live")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var runs []*models.BuildRun
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, err
	}

	return runs, nil
}

// GetBuildRun fetches details for a specific build run
func (c *APIClient) GetBuildRun(runID string) (*models.BuildRun, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/runs/" + runID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("build run not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var run models.BuildRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return nil, err
	}

	return &run, nil
}

// GetBuildEvents fetches events for a specific build run
func (c *APIClient) GetBuildEvents(runID string) ([]*models.BuildEvent, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/runs/" + runID + "/events")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var events []*models.BuildEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}

	return events, nil
}

// GetBuildProcesses fetches processes for a specific build run
func (c *APIClient) GetBuildProcesses(runID string) ([]*models.ProcessInfo, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/runs/" + runID + "/processes")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var processes []*models.ProcessInfo
	if err := json.NewDecoder(resp.Body).Decode(&processes); err != nil {
		return nil, err
	}

	return processes, nil
}

// GetStats fetches daemon statistics
func (c *APIClient) GetStats() (*StatsResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var stats StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}

	return &stats, nil
}

// DeleteBuildRun deletes a single build run
func (c *APIClient) DeleteBuildRun(runID string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+"/api/runs/"+runID, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("build run not found")
	}
	if resp.StatusCode == http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cannot delete: %s", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: %s", resp.Status)
	}

	return nil
}

// DeleteBuildRuns deletes multiple build runs
func (c *APIClient) DeleteBuildRuns(runIDs []string) (*DeleteResponse, error) {
	requestBody := map[string][]string{
		"run_ids": runIDs,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(c.baseURL+"/api/delete", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var deleteResp DeleteResponse
	if err := json.NewDecoder(resp.Body).Decode(&deleteResp); err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return &deleteResp, fmt.Errorf("API error: %s", resp.Status)
	}

	return &deleteResp, nil
}

// CheckDaemonHealth checks if the daemon is running and accessible
func (c *APIClient) CheckDaemonHealth() error {
	resp, err := c.httpClient.Get(c.baseURL + "/api/stats")
	if err != nil {
		return fmt.Errorf("daemon not accessible: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon health check failed: %s", resp.Status)
	}

	return nil
}
