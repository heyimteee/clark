package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type MacosClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewMacosClient(baseURL, token string) *MacosClient {
	return &MacosClient{
		baseURL: baseURL,
		token:   token,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *MacosClient) List(ctx context.Context, from, to time.Time) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/calendars/events?from=%s&to=%s", c.baseURL, from.Format(time.RFC3339), to.Format(time.RFC3339)), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calendar list failed: %s", resp.Status)
	}
	var out struct {
		Events []Event `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

func (c *MacosClient) Create(ctx context.Context, e Event) (string, error) {
	body, _ := json.Marshal(e)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/calendars/events", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("calendar create failed: %s", resp.Status)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *MacosClient) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/calendars/events/"+id, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("calendar delete failed: %s", resp.Status)
	}
	return nil
}

func (c *MacosClient) auth(r *http.Request) {
	if c.token != "" {
		r.Header.Set("X-Clark-Bridge-Token", c.token)
	}
}
