package main

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// snapshot is the most recent successfully fetched API state. The poller
// swaps a whole snapshot at once so a Prometheus scrape always sees a
// consistent view.
type snapshot struct {
	species     []Species
	stats       *Stats
	fetchedAt   time.Time
	fetchTook   time.Duration
	lastSuccess bool
}

// Poller queries the BirdWeather API on a fixed interval and caches the
// result for the collector.
type Poller struct {
	client   *Client
	period   string
	limit    int
	interval time.Duration

	mu   sync.RWMutex
	snap snapshot
}

func NewPoller(client *Client, period string, limit int, interval time.Duration) *Poller {
	return &Poller{client: client, period: period, limit: limit, interval: interval}
}

// Run polls immediately, then on every interval tick until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.poll(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	start := time.Now()
	fetchCtx, cancel := context.WithTimeout(ctx, p.interval)
	defer cancel()

	species, sErr := p.client.FetchSpecies(fetchCtx, p.period, p.limit)
	stats, stErr := p.client.FetchStats(fetchCtx, p.period)
	took := time.Since(start)

	if sErr != nil || stErr != nil {
		if sErr != nil {
			log.Printf("error fetching species: %v", sErr)
		}
		if stErr != nil {
			log.Printf("error fetching stats: %v", stErr)
		}
		p.mu.Lock()
		p.snap.lastSuccess = false
		p.snap.fetchTook = took
		p.mu.Unlock()
		return
	}

	log.Printf("fetched %d species, %.0f detections (period=%s) in %s",
		len(species), stats.Detections, p.period, took.Round(time.Millisecond))

	p.mu.Lock()
	p.snap = snapshot{
		species:     species,
		stats:       stats,
		fetchedAt:   time.Now(),
		fetchTook:   took,
		lastSuccess: true,
	}
	p.mu.Unlock()
}

func (p *Poller) snapshot() snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}

// Collector exposes the poller's cached snapshot as Prometheus metrics.
// Const metrics are rebuilt on every scrape, so species that disappear from
// the API response (e.g. after the period window rolls over) drop out of the
// exposition automatically.
type Collector struct {
	poller *Poller
	period string

	detectionCount       *prometheus.Desc
	confidenceCount      *prometheus.Desc
	latestDetection      *prometheus.Desc
	stationDetections    *prometheus.Desc
	stationSpeciesCount  *prometheus.Desc
	querySuccess         *prometheus.Desc
	queryDuration        *prometheus.Desc
	lastSuccessTimestamp *prometheus.Desc
}

func NewCollector(poller *Poller, period string) *Collector {
	speciesLabels := []string{"species", "scientific_name", "species_id"}
	periodLabel := prometheus.Labels{"period": period}
	return &Collector{
		poller: poller,
		period: period,
		detectionCount: prometheus.NewDesc(
			"birdweather_detections_count",
			"Number of detections of a species reported by the station over the configured period.",
			speciesLabels, periodLabel),
		confidenceCount: prometheus.NewDesc(
			"birdweather_detections_confidence_count",
			"Number of detections of a species broken down by BirdWeather confidence bucket.",
			append(speciesLabels, "confidence"), periodLabel),
		latestDetection: prometheus.NewDesc(
			"birdweather_species_latest_detection_timestamp_seconds",
			"Unix timestamp of the most recent detection of a species.",
			speciesLabels, nil),
		stationDetections: prometheus.NewDesc(
			"birdweather_station_detections_count",
			"Total number of detections reported by the station over the configured period.",
			nil, periodLabel),
		stationSpeciesCount: prometheus.NewDesc(
			"birdweather_station_species_count",
			"Number of unique species detected by the station over the configured period.",
			nil, periodLabel),
		querySuccess: prometheus.NewDesc(
			"birdweather_query_success",
			"Whether the most recent BirdWeather API query succeeded (1) or failed (0).",
			nil, nil),
		queryDuration: prometheus.NewDesc(
			"birdweather_query_duration_seconds",
			"Duration of the most recent BirdWeather API query.",
			nil, nil),
		lastSuccessTimestamp: prometheus.NewDesc(
			"birdweather_last_successful_query_timestamp_seconds",
			"Unix timestamp of the last successful BirdWeather API query.",
			nil, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.detectionCount
	ch <- c.confidenceCount
	ch <- c.latestDetection
	ch <- c.stationDetections
	ch <- c.stationSpeciesCount
	ch <- c.querySuccess
	ch <- c.queryDuration
	ch <- c.lastSuccessTimestamp
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	snap := c.poller.snapshot()

	success := 0.0
	if snap.lastSuccess {
		success = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.querySuccess, prometheus.GaugeValue, success)
	ch <- prometheus.MustNewConstMetric(c.queryDuration, prometheus.GaugeValue, snap.fetchTook.Seconds())

	if snap.fetchedAt.IsZero() {
		// No successful query yet: expose only the meta metrics.
		return
	}
	ch <- prometheus.MustNewConstMetric(c.lastSuccessTimestamp, prometheus.GaugeValue, float64(snap.fetchedAt.Unix()))

	if snap.stats != nil {
		ch <- prometheus.MustNewConstMetric(c.stationDetections, prometheus.GaugeValue, snap.stats.Detections)
		ch <- prometheus.MustNewConstMetric(c.stationSpeciesCount, prometheus.GaugeValue, snap.stats.Species)
	}

	for i := range snap.species {
		sp := &snap.species[i]
		labels := []string{sp.CommonName, sp.ScientificName, strconv.Itoa(sp.ID)}

		ch <- prometheus.MustNewConstMetric(c.detectionCount, prometheus.GaugeValue, sp.Detections.Total, labels...)

		if sp.Detections.HasBreakdown() {
			for confidence, v := range map[string]float64{
				"almost_certain": sp.Detections.AlmostCertain,
				"very_likely":    sp.Detections.VeryLikely,
				"uncertain":      sp.Detections.Uncertain,
				"unlikely":       sp.Detections.Unlikely,
			} {
				ch <- prometheus.MustNewConstMetric(c.confidenceCount, prometheus.GaugeValue, v, append(labels, confidence)...)
			}
		}

		if t := sp.LatestDetectionTime(); !t.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.latestDetection, prometheus.GaugeValue, float64(t.Unix()), labels...)
		}
	}
}
