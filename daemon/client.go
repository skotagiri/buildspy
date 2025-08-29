package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"buildspy/models"
)

// Client communicates with the buildspyd daemon
type Client struct {
	baseURL string
	client  *http.Client
}

// NewClient creates a new daemon client
func NewClient(port int) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://localhost:%d", port),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// IsRunning checks if the daemon is running
func (c *Client) IsRunning() bool {
	resp, err := c.client.Get(c.baseURL + "/api/stats")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// SubmitBuildRun sends a build run to the daemon
func (c *Client) SubmitBuildRun(buildRun *models.BuildRun) error {
	return c.submit("build_run", buildRun)
}

// SubmitBuildEvent sends a build event to the daemon
func (c *Client) SubmitBuildEvent(event *models.BuildEvent) error {
	return c.submit("build_event", event)
}

// SubmitProcess sends process info to the daemon
func (c *Client) SubmitProcess(process *models.ProcessInfo) error {
	return c.submit("process", process)
}

// submit sends data to the daemon's live submission endpoint
func (c *Client) submit(dataType string, data interface{}) error {
	submission := map[string]interface{}{
		"type": dataType,
		"data": data,
	}

	jsonData, err := json.Marshal(submission)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	resp, err := c.client.Post(
		c.baseURL+"/api/live/submit",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return fmt.Errorf("failed to send data to daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned error: %s", resp.Status)
	}

	return nil
}