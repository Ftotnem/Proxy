package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time" // For context timeout

	"github.com/Ftotnem/Backend/go/shared/models" // Your shared models
)

type PlayerDataServiceClient struct {
	baseURL    string // e.g., "http://localhost:8081"
	httpClient *http.Client
}

// NewPlayerDataServiceClient creates a new client.
func NewPlayerDataServiceClient(baseURL string) *PlayerDataServiceClient {
	return &PlayerDataServiceClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second}, // Adjust timeout as needed
	}
}

// GetPlayer fetches a player's profile by UUID.
// Returns nil and an error if not found or other issues.
func (c *PlayerDataServiceClient) GetPlayer(ctx context.Context, uuid string) (*models.Player, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/players/%s", c.baseURL, uuid), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create get player request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make get player request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("player %s not found", uuid) // Specific error for "not found"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code for get player: %d", resp.StatusCode)
	}

	var player models.Player
	if err := json.NewDecoder(resp.Body).Decode(&player); err != nil {
		return nil, fmt.Errorf("failed to decode get player response: %w", err)
	}

	return &player, nil
}

// CreatePlayer creates a new player profile.
func (c *PlayerDataServiceClient) CreatePlayer(ctx context.Context, player *models.Player) error {
	body, err := json.Marshal(player)
	if err != nil {
		return fmt.Errorf("failed to marshal create player request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/players", c.baseURL), bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create create player request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make create player request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK { // 201 Created or 200 OK for upsert
		return fmt.Errorf("unexpected status code for create player: %d", resp.StatusCode)
	}

	return nil
}
