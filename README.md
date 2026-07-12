# BirdWeather Prometheus Exporter

A Prometheus exporter for [BirdWeather](https://www.birdweather.com/) stations
(e.g. the BirdWeather PUC). It periodically queries the
[BirdWeather station API](https://app.birdweather.com/api/index.html) and
exposes bird detection counts as Prometheus metrics, so you can graph and
alert on your backyard birds.

The exporter polls the API on a configurable interval and caches the result,
so Prometheus can scrape `/metrics` as often as it likes without hammering the
BirdWeather API.

## Metrics

| Metric | Labels | Description |
| ------ | ------ | ----------- |
| `birdweather_detections_count` | `species`, `scientific_name`, `species_id`, `period` | Number of detections of a species over the configured period. |
| `birdweather_detections_confidence_count` | `species`, `scientific_name`, `species_id`, `confidence`, `period` | Detections broken down by BirdWeather confidence bucket (`almost_certain`, `very_likely`, `uncertain`, `unlikely`). Only exposed when the API provides the breakdown. |
| `birdweather_species_latest_detection_timestamp_seconds` | `species`, `scientific_name`, `species_id` | Unix timestamp of the most recent detection of a species. |
| `birdweather_station_detections_count` | `period` | Total detections reported by the station over the period. |
| `birdweather_station_species_count` | `period` | Number of unique species detected over the period. |
| `birdweather_query_success` | | 1 if the most recent API query succeeded, 0 otherwise. |
| `birdweather_query_duration_seconds` | | Duration of the most recent API query. |
| `birdweather_last_successful_query_timestamp_seconds` | | Unix timestamp of the last successful API query. |

All detection counts are **gauges**, not counters: the API reports counts over
a rolling window (`day` by default), so values go down as old detections age
out of the window. Standard Go and process metrics are also exported.

## Configuration

Every option can be set with a flag or an environment variable (flags win).
Only the station token is required — find it in the BirdWeather app or on
your station page at `app.birdweather.com`.

| Flag | Environment variable | Default | Description |
| ---- | -------------------- | ------- | ----------- |
| `--station-token` | `BIRDWEATHER_STATION_TOKEN` | — | **Required.** Your BirdWeather station token. |
| `--query-interval` | `BIRDWEATHER_QUERY_INTERVAL` | `5m` | How often to query the BirdWeather API (minimum `30s`). |
| `--period` | `BIRDWEATHER_PERIOD` | `day` | Window the counts cover: `day`, `week`, `month` or `all`. |
| `--species-limit` | `BIRDWEATHER_SPECIES_LIMIT` | `100` | Max species to fetch per query (API max is 100). |
| `--listen-address` | `BIRDWEATHER_LISTEN_ADDRESS` | `:9743` | Address of the metrics HTTP server. |
| `--api-base-url` | `BIRDWEATHER_API_BASE_URL` | `https://app.birdweather.com/api/v1` | Override for testing. |

## Running

### Docker

Released versions are published to GitHub Container Registry as multi-arch
images (linux/amd64 and linux/arm64):

```sh
docker run -d --name birdweather-exporter \
  -e BIRDWEATHER_STATION_TOKEN=your-station-token \
  -e BIRDWEATHER_QUERY_INTERVAL=5m \
  -p 9743:9743 \
  ghcr.io/chaukap/birdweather-prometheus-exporter:latest
```

Then check `http://localhost:9743/metrics`. Pin a version tag (e.g. `:1.0.0`)
in anything long-lived.

To build locally instead:

```sh
docker build -t birdweather-prometheus-exporter .
```

The image is a distroless build; use
`docker buildx build --platform linux/amd64,linux/arm64 ...` if you need an
arm64 image (e.g. for a Raspberry Pi running Home Assistant OS).

### Kubernetes

An example Deployment, Service and Secret are in
[`examples/kubernetes.yaml`](examples/kubernetes.yaml):

```sh
kubectl apply -f examples/kubernetes.yaml
```

Edit the Secret to contain your real station token first, and point the
`image:` at wherever you push the built image.

### Home Assistant

Any way of running a container alongside Home Assistant works — for
Home Assistant OS/Supervised, run it on the same host (or any machine on your
network) with the `docker run` command above, then let your Prometheus
instance scrape it.

### From source

```sh
go build .
./birdweather-prometheus-exporter --station-token your-station-token
```

## Grafana dashboard

A ready-made dashboard lives in
[`grafana/birdweather-dashboard.json`](grafana/birdweather-dashboard.json):
species leaderboard, most recent visitors, PUC status and link history,
activity over time, and detection-confidence breakdown.

Import it in Grafana via **Dashboards → New → Import**, paste or upload the
JSON, and pick your Prometheus data source when prompted. With the Grafana
Home Assistant add-on you can instead drop the file into the add-on's
provisioning directory (`/addon_configs/<hash>_grafana/provisioning/dashboards/`)
next to a small [dashboard provider][grafana-provisioning] YAML.

Counts are rolling-window gauges (see above), so panels show the window's
current value rather than lifetime totals.

[grafana-provisioning]: https://grafana.com/docs/grafana/latest/administration/provisioning/#dashboards

## Prometheus scrape config

```yaml
scrape_configs:
  - job_name: birdweather
    scrape_interval: 1m
    static_configs:
      - targets: ["birdweather-exporter:9743"]
```

Example queries:

```promql
# Top 10 species today
topk(10, birdweather_detections_count)

# Seconds since a Northern Cardinal was last heard
time() - birdweather_species_latest_detection_timestamp_seconds{species="Northern Cardinal"}

# Alert if the exporter can't reach the BirdWeather API
birdweather_query_success == 0
```

## Endpoints

- `/metrics` — Prometheus metrics
- `/healthz` — liveness/readiness probe, always returns `200 ok`

## CI/CD

Three GitHub Actions workflows keep the pipeline honest:

- **CI** (`ci.yml`) — on every pull request to `main` (and pushes to `main`):
  runs `go vet` and the test suite with the race detector, verifies the Docker
  image builds for both amd64 and arm64, and runs Trivy scans of the
  repository (dependencies, secrets, misconfigurations) and the built image.
  Fixable HIGH/CRITICAL findings fail the check, so they can't merge.
- **Release** (`release.yml`) — when a GitHub release is published: re-runs
  tests, Trivy-scans the image *before* anything is pushed, then builds and
  pushes the multi-arch image to GHCR tagged with the release version
  (a `v1.2.3` release produces `1.2.3`, `1.2`, `v1.2.3` and `latest`).
  Nothing is ever uploaded from pull requests or branch pushes.
- **Scheduled scan** (`scheduled-scan.yml`) — weekly (and on demand via
  workflow dispatch): Trivy-scans the repository and the latest published
  image, and reports findings to the repository's Security tab. This catches
  CVEs discovered *after* an image was released.

To cut a release: create a GitHub release with a semver tag like `v1.0.0`.
The workflow publishes `ghcr.io/chaukap/birdweather-prometheus-exporter`
automatically using the built-in `GITHUB_TOKEN` — no registry credentials to
manage. After the first release, make the package public once in the GHCR
package settings (GitHub creates new packages as private by default).
