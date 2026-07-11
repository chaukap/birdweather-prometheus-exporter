package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to the BirdWeather station API
// (https://app.birdweather.com/api/index.html). All station endpoints are
// authenticated by embedding the station token in the URL path, so the token
// must never appear in logs or error messages.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// DetectionCounts holds the per-species detection counts for a period. The
// API returns either a plain number or an object broken down by confidence
// bucket, depending on endpoint version; both forms are accepted.
type DetectionCounts struct {
	Total         float64
	AlmostCertain float64
	VeryLikely    float64
	Uncertain     float64
	Unlikely      float64
	hasBreakdown  bool
}

func (d *DetectionCounts) HasBreakdown() bool { return d.hasBreakdown }

func (d *DetectionCounts) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		total, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return fmt.Errorf("detections is neither a number nor an object: %w", err)
		}
		d.Total = total
		return nil
	}
	var obj struct {
		Total         float64 `json:"total"`
		AlmostCertain float64 `json:"almostCertain"`
		VeryLikely    float64 `json:"veryLikely"`
		Uncertain     float64 `json:"uncertain"`
		Unlikely      float64 `json:"unlikely"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	d.Total = obj.Total
	d.AlmostCertain = obj.AlmostCertain
	d.VeryLikely = obj.VeryLikely
	d.Uncertain = obj.Uncertain
	d.Unlikely = obj.Unlikely
	d.hasBreakdown = true
	return nil
}

// Species is one entry from GET /stations/{token}/species.
type Species struct {
	ID                int             `json:"id"`
	CommonName        string          `json:"commonName"`
	ScientificName    string          `json:"scientificName"`
	Detections        DetectionCounts `json:"detections"`
	LatestDetectionAt string          `json:"latestDetectionAt"`
}

// LatestDetectionTime parses latestDetectionAt, returning the zero time when
// the field is absent or unparseable.
func (s *Species) LatestDetectionTime() time.Time {
	if s.LatestDetectionAt == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000-07:00"} {
		if t, err := time.Parse(layout, s.LatestDetectionAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

type speciesResponse struct {
	Success bool      `json:"success"`
	Species []Species `json:"species"`
}

// Stats is the response of GET /stations/{token}/stats.
type Stats struct {
	Success    bool    `json:"success"`
	Detections float64 `json:"detections"`
	Species    float64 `json:"species"`
}

// FetchSpecies returns the species detected by the station during the given
// period (day/week/month/all), with detection counts, ordered by count.
func (c *Client) FetchSpecies(ctx context.Context, period string, limit int) ([]Species, error) {
	q := url.Values{}
	q.Set("period", period)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("sort", "top")

	var resp speciesResponse
	if err := c.get(ctx, "/species", q, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("species endpoint returned success=false")
	}
	return resp.Species, nil
}

// FetchStats returns the station-wide detection and species totals for the
// given period.
func (c *Client) FetchStats(ctx context.Context, period string) (*Stats, error) {
	q := url.Values{}
	q.Set("period", period)

	var resp Stats
	if err := c.get(ctx, "/stats", q, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("stats endpoint returned success=false")
	}
	return &resp, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := fmt.Sprintf("%s/stations/%s%s?%s", c.baseURL, url.PathEscape(c.token), path, query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "birdweather-prometheus-exporter")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Unwrap *url.Error so the station token embedded in the URL is
		// never leaked into logs.
		if uerr, ok := err.(*url.Error); ok {
			return fmt.Errorf("GET %s: %w", path, uerr.Err)
		}
		return fmt.Errorf("GET %s failed", path)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("GET %s: reading body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %s", path, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: decoding JSON: %w", path, err)
	}
	return nil
}
