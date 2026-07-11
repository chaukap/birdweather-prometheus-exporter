package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const defaultAPIBaseURL = "https://app.birdweather.com/api/v1"

type config struct {
	stationToken  string
	queryInterval time.Duration
	period        string
	speciesLimit  int
	listenAddress string
	apiBaseURL    string
}

// envOr returns the value of the environment variable name, or fallback when
// it is unset or empty. Flags take precedence over environment variables
// because env values are used as flag defaults.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func parseConfig(args []string) (*config, error) {
	defaultInterval, err := time.ParseDuration(envOr("BIRDWEATHER_QUERY_INTERVAL", "5m"))
	if err != nil {
		return nil, fmt.Errorf("invalid BIRDWEATHER_QUERY_INTERVAL: %w", err)
	}
	defaultLimit, err := strconv.Atoi(envOr("BIRDWEATHER_SPECIES_LIMIT", "100"))
	if err != nil {
		return nil, fmt.Errorf("invalid BIRDWEATHER_SPECIES_LIMIT: %w", err)
	}

	cfg := &config{}
	fs := flag.NewFlagSet("birdweather-prometheus-exporter", flag.ContinueOnError)
	fs.StringVar(&cfg.stationToken, "station-token", os.Getenv("BIRDWEATHER_STATION_TOKEN"),
		"BirdWeather station token (env: BIRDWEATHER_STATION_TOKEN). Required.")
	fs.DurationVar(&cfg.queryInterval, "query-interval", defaultInterval,
		"How often to query the BirdWeather API (env: BIRDWEATHER_QUERY_INTERVAL).")
	fs.StringVar(&cfg.period, "period", envOr("BIRDWEATHER_PERIOD", "day"),
		"Detection period the counts cover: day, week, month or all (env: BIRDWEATHER_PERIOD).")
	fs.IntVar(&cfg.speciesLimit, "species-limit", defaultLimit,
		"Maximum number of species to fetch, API max is 100 (env: BIRDWEATHER_SPECIES_LIMIT).")
	fs.StringVar(&cfg.listenAddress, "listen-address", envOr("BIRDWEATHER_LISTEN_ADDRESS", ":9743"),
		"Address for the metrics HTTP server (env: BIRDWEATHER_LISTEN_ADDRESS).")
	fs.StringVar(&cfg.apiBaseURL, "api-base-url", envOr("BIRDWEATHER_API_BASE_URL", defaultAPIBaseURL),
		"Base URL of the BirdWeather API (env: BIRDWEATHER_API_BASE_URL).")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if cfg.stationToken == "" {
		return nil, errors.New("a station token is required: set --station-token or BIRDWEATHER_STATION_TOKEN")
	}
	switch cfg.period {
	case "day", "week", "month", "all":
	default:
		return nil, fmt.Errorf("invalid period %q: must be day, week, month or all", cfg.period)
	}
	if cfg.queryInterval < 30*time.Second {
		return nil, fmt.Errorf("query interval %s is too short: minimum is 30s to stay friendly to the BirdWeather API", cfg.queryInterval)
	}
	if cfg.speciesLimit < 1 || cfg.speciesLimit > 100 {
		return nil, fmt.Errorf("species limit %d out of range: must be 1-100", cfg.speciesLimit)
	}
	return cfg, nil
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Fatalf("configuration error: %v", err)
	}

	client := NewClient(cfg.apiBaseURL, cfg.stationToken, 30*time.Second)
	poller := NewPoller(client, cfg.period, cfg.speciesLimit, cfg.queryInterval)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		NewCollector(poller, cfg.period),
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go poller.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>BirdWeather Exporter</title></head>
<body><h1>BirdWeather Prometheus Exporter</h1><p><a href="/metrics">Metrics</a></p></body></html>`)
	})

	server := &http.Server{Addr: cfg.listenAddress, Handler: mux}
	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("querying BirdWeather API every %s (period=%s), serving metrics on %s/metrics",
		cfg.queryInterval, cfg.period, cfg.listenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server error: %v", err)
	}
}
