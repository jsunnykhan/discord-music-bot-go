package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SpotifyClient handles Spotify Web API authentication (Client Credentials)
// and track metadata lookups.
type SpotifyClient struct {
	clientID     string
	clientSecret string
	token        string
	tokenExpiry  time.Time
	mu           sync.Mutex
	httpClient   *http.Client
}

// NewSpotifyClient creates a new SpotifyClient.
func NewSpotifyClient(clientID, clientSecret string) *SpotifyClient {
	return &SpotifyClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ParseSpotifyTrackID extracts a Spotify track ID from a URL.
// Example: https://open.spotify.com/track/4PTG3Z6ehGkBF3zIqYQGS3?si=xxx → 4PTG3Z6ehGkBF3zIqYQGS3
func ParseSpotifyTrackID(input string) (string, bool) {
	re := regexp.MustCompile(`open\.spotify\.com/track/([a-zA-Z0-9]+)`)
	m := re.FindStringSubmatch(input)
	if len(m) > 1 {
		return m[1], true
	}
	return "", false
}

// authenticate obtains a fresh OAuth token via the Client Credentials flow.
func (s *SpotifyClient) authenticate() error {
	if s.clientID == "" || s.clientSecret == "" {
		return fmt.Errorf("spotify credentials are not configured")
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")

	req, err := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
		[]byte(s.clientID+":"+s.clientSecret),
	))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("spotify token request failed (%d): %s", resp.StatusCode, body)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return err
	}

	s.token = tok.AccessToken
	s.tokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn-60) * time.Second)
	return nil
}

// getValidToken returns a cached token or refreshes it when expired.
func (s *SpotifyClient) getValidToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token == "" || time.Now().After(s.tokenExpiry) {
		if err := s.authenticate(); err != nil {
			return "", err
		}
	}
	return s.token, nil
}

// GetTrackMetadata fetches the title and joined artist names for a track ID.
func (s *SpotifyClient) GetTrackMetadata(trackID string) (title, artist string, err error) {
	token, err := s.getValidToken()
	if err != nil {
		return "", "", fmt.Errorf("obtaining spotify token: %w", err)
	}

	req, err := http.NewRequest("GET", "https://api.spotify.com/v1/tracks/"+trackID, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("spotify API (%d): %s", resp.StatusCode, body)
	}

	var info struct {
		Name    string `json:"name"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", err
	}

	names := make([]string, len(info.Artists))
	for i, a := range info.Artists {
		names[i] = a.Name
	}
	return info.Name, strings.Join(names, ", "), nil
}
