package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const fakeToken = "test-token-123"

func fakeAPI(t *testing.T, speciesJSON, statsJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/stations/%s/species", fakeToken), func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("period"); got != "day" {
			t.Errorf("species: period = %q, want day", got)
		}
		if got := r.URL.Query().Get("limit"); got != "100" {
			t.Errorf("species: limit = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, speciesJSON)
	})
	mux.HandleFunc(fmt.Sprintf("/stations/%s/stats", fakeToken), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, statsJSON)
	})
	return httptest.NewServer(mux)
}

const speciesWithBreakdown = `{
  "success": true,
  "species": [
    {
      "id": 144,
      "commonName": "Northern Cardinal",
      "scientificName": "Cardinalis cardinalis",
      "detections": {"total": 42, "almostCertain": 30, "veryLikely": 10, "uncertain": 1, "unlikely": 1},
      "latestDetectionAt": "2026-07-11T08:15:00-06:00"
    },
    {
      "id": 12,
      "commonName": "Blue Jay",
      "scientificName": "Cyanocitta cristata",
      "detections": {"total": 7, "almostCertain": 5, "veryLikely": 2, "uncertain": 0, "unlikely": 0}
    }
  ]
}`

const speciesWithPlainCounts = `{
  "success": true,
  "species": [
    {"id": 1, "commonName": "House Finch", "scientificName": "Haemorhous mexicanus", "detections": 13}
  ]
}`

const statsJSON = `{"success": true, "detections": 49, "species": 2}`

func newTestCollector(t *testing.T, apiURL string) *Collector {
	t.Helper()
	client := NewClient(apiURL, fakeToken, 5*time.Second)
	poller := NewPoller(client, "day", 100, time.Minute)
	poller.poll(context.Background())
	return NewCollector(poller, "day")
}

func TestExportsSpeciesCounts(t *testing.T) {
	server := fakeAPI(t, speciesWithBreakdown, statsJSON)
	defer server.Close()
	c := newTestCollector(t, server.URL)

	expected := `
# HELP birdweather_detections_count Number of detections of a species reported by the station over the configured period.
# TYPE birdweather_detections_count gauge
birdweather_detections_count{period="day",scientific_name="Cardinalis cardinalis",species="Northern Cardinal",species_id="144"} 42
birdweather_detections_count{period="day",scientific_name="Cyanocitta cristata",species="Blue Jay",species_id="12"} 7
# HELP birdweather_station_detections_count Total number of detections reported by the station over the configured period.
# TYPE birdweather_station_detections_count gauge
birdweather_station_detections_count{period="day"} 49
# HELP birdweather_station_species_count Number of unique species detected by the station over the configured period.
# TYPE birdweather_station_species_count gauge
birdweather_station_species_count{period="day"} 2
# HELP birdweather_query_success Whether the most recent BirdWeather API query succeeded (1) or failed (0).
# TYPE birdweather_query_success gauge
birdweather_query_success 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"birdweather_detections_count",
		"birdweather_station_detections_count",
		"birdweather_station_species_count",
		"birdweather_query_success",
	); err != nil {
		t.Error(err)
	}
}

func TestExportsConfidenceBreakdownAndTimestamp(t *testing.T) {
	server := fakeAPI(t, speciesWithBreakdown, statsJSON)
	defer server.Close()
	c := newTestCollector(t, server.URL)

	expected := `
# HELP birdweather_detections_confidence_count Number of detections of a species broken down by BirdWeather confidence bucket.
# TYPE birdweather_detections_confidence_count gauge
birdweather_detections_confidence_count{confidence="almost_certain",period="day",scientific_name="Cardinalis cardinalis",species="Northern Cardinal",species_id="144"} 30
birdweather_detections_confidence_count{confidence="very_likely",period="day",scientific_name="Cardinalis cardinalis",species="Northern Cardinal",species_id="144"} 10
birdweather_detections_confidence_count{confidence="uncertain",period="day",scientific_name="Cardinalis cardinalis",species="Northern Cardinal",species_id="144"} 1
birdweather_detections_confidence_count{confidence="unlikely",period="day",scientific_name="Cardinalis cardinalis",species="Northern Cardinal",species_id="144"} 1
birdweather_detections_confidence_count{confidence="almost_certain",period="day",scientific_name="Cyanocitta cristata",species="Blue Jay",species_id="12"} 5
birdweather_detections_confidence_count{confidence="very_likely",period="day",scientific_name="Cyanocitta cristata",species="Blue Jay",species_id="12"} 2
birdweather_detections_confidence_count{confidence="uncertain",period="day",scientific_name="Cyanocitta cristata",species="Blue Jay",species_id="12"} 0
birdweather_detections_confidence_count{confidence="unlikely",period="day",scientific_name="Cyanocitta cristata",species="Blue Jay",species_id="12"} 0
# HELP birdweather_species_latest_detection_timestamp_seconds Unix timestamp of the most recent detection of a species.
# TYPE birdweather_species_latest_detection_timestamp_seconds gauge
birdweather_species_latest_detection_timestamp_seconds{scientific_name="Cardinalis cardinalis",species="Northern Cardinal",species_id="144"} 1.7837793e+09
`
	// 1.7837793e9 = 2026-07-11T14:15:00Z, i.e. 08:15:00-06:00 from the fixture.
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"birdweather_detections_confidence_count",
		"birdweather_species_latest_detection_timestamp_seconds",
	); err != nil {
		t.Error(err)
	}
}

func TestPlainNumberDetections(t *testing.T) {
	server := fakeAPI(t, speciesWithPlainCounts, statsJSON)
	defer server.Close()
	c := newTestCollector(t, server.URL)

	expected := `
# HELP birdweather_detections_count Number of detections of a species reported by the station over the configured period.
# TYPE birdweather_detections_count gauge
birdweather_detections_count{period="day",scientific_name="Haemorhous mexicanus",species="House Finch",species_id="1"} 13
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"birdweather_detections_count", "birdweather_detections_confidence_count",
	); err != nil {
		t.Error(err)
	}
}

func TestFailedQueryReportsZeroSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	c := newTestCollector(t, server.URL)

	expected := `
# HELP birdweather_query_success Whether the most recent BirdWeather API query succeeded (1) or failed (0).
# TYPE birdweather_query_success gauge
birdweather_query_success 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"birdweather_query_success",
		"birdweather_detections_count",
		"birdweather_last_successful_query_timestamp_seconds",
	); err != nil {
		t.Error(err)
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "super-secret-token", time.Second)
	_, err := client.FetchSpecies(context.Background(), "day", 100)
	if err == nil {
		t.Fatal("expected error from unreachable server")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("error message leaks station token: %v", err)
	}
}

func TestParseConfig(t *testing.T) {
	t.Setenv("BIRDWEATHER_STATION_TOKEN", "")

	if _, err := parseConfig(nil); err == nil {
		t.Error("expected error when no token is configured")
	}

	cfg, err := parseConfig([]string{"--station-token", "abc", "--query-interval", "1m", "--period", "week"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.queryInterval != time.Minute || cfg.period != "week" {
		t.Errorf("got interval=%s period=%s", cfg.queryInterval, cfg.period)
	}

	if _, err := parseConfig([]string{"--station-token", "abc", "--query-interval", "5s"}); err == nil {
		t.Error("expected error for too-short interval")
	}
	if _, err := parseConfig([]string{"--station-token", "abc", "--period", "year"}); err == nil {
		t.Error("expected error for invalid period")
	}

	t.Setenv("BIRDWEATHER_STATION_TOKEN", "from-env")
	t.Setenv("BIRDWEATHER_QUERY_INTERVAL", "10m")
	cfg, err = parseConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.stationToken != "from-env" || cfg.queryInterval != 10*time.Minute {
		t.Errorf("env config not applied: token=%q interval=%s", cfg.stationToken, cfg.queryInterval)
	}
}
