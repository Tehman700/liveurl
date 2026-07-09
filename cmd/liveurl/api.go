package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/Tehman700/liveurl/internal/cliconfig"
)

// apiClient is a thin wrapper for calling liveurld's control API with the
// locally saved credentials.
type apiClient struct {
	baseURL string
	token   string
}

func newAPIClient() (*apiClient, error) {
	creds, err := cliconfig.Load()
	if err != nil {
		return nil, err
	}
	if creds.Token == "" {
		return nil, fmt.Errorf("not logged in — run `liveurl login <token>` first (see `liveurld seed`)")
	}
	return &apiClient{baseURL: creds.ControlURL, token: creds.Token}, nil
}

func (c *apiClient) do(method, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", u, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Error != "" {
			return fmt.Errorf("%s (status %d)", apiErr.Error, resp.StatusCode)
		}
		return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}
