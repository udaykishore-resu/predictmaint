# Runbook

## Service level objectives

| SLO | Target | Measured by |
|---|---|---|
| Ingest availability | 99.9% of `POST /v1/metrics` and Kafka batches succeed | `predictmaint_http_requests_total{route="POST /v1/metrics"}`, `predictmaint_kafka_batches_total` |
| Evaluation latency | p99 < 250 ms per ingest batch | `predictmaint_http_request_duration_seconds{route="POST /v1/metrics"}` |
| Alert precision proxy | ≥ 0.7 per site over 30 days | `predictmaint_precision_proxy` |
| Alert volume | ≤ 0.2 alerts/asset/day per site | `predictmaint_alerts_per_day` / `predictmaint_assets_tracked` |
| Time to acknowledge | MTTA < 4 h | `predictmaint_mtta_seconds` |

## Key metrics

- `predictmaint_metrics_ingested_total{result}` accepted / rejected / duplicate.
  A rising `rejected` rate means a gateway is sending wrong units or
  unknown signals; the per-metric reasons are in the HTTP response and in
  `kafka batch partially rejected` warnings.
- `predictmaint_evaluations_total{class}` and `predictmaint_risk_score{class}` histogram.
- `predictmaint_alert_outcomes_total{site,class,outcome}`: `fire`,
  `pending`, `suppressed`, `rate_limited`, `clear`. A high
  `rate_limited`/`fire` ratio means the site limit is too low or a site is
  genuinely on fire.
- `predictmaint_work_orders_total{site,class,result}`: `created`,
  `skipped` (dedupe / below confidence), `failed` (CMMS), `cancelled`.
- `predictmaint_feedback_total{class,verdict}`.
- `predictmaint_precision_proxy{site}`, `predictmaint_alerts_per_day{site}`,
  `predictmaint_mtta_seconds{site}` (refreshed every 30 s and on `GET /v1/stats`).
- RED: `predictmaint_http_requests_total`, `predictmaint_http_request_duration_seconds`,
  `predictmaint_http_requests_in_flight`.

## Alerts (Prometheus rules, suggested)

| Alert | Expression | Severity |
|---|---|---|
| CMMSFailing | `increase(predictmaint_work_orders_total{result="failed"}[15m]) > 0` | page |
| IngestRejects | `rate(predictmaint_metrics_ingested_total{result="rejected"}[10m]) / rate(predictmaint_metrics_ingested_total[10m]) > 0.05` | ticket |
| AlertStorm | `sum by (site) (increase(predictmaint_alert_outcomes_total{outcome="fire"}[1h])) > 10` | page |
| RateLimitedSite | `increase(predictmaint_alert_outcomes_total{outcome="rate_limited"}[1h]) > 0` | ticket |
| PrecisionLow | `predictmaint_precision_proxy < 0.5 and predictmaint_feedback_total > 20` | ticket |
| NotReady | `up == 0` or readiness failing for 5 m | page |

## Dashboards

1. **Fleet**: risk histogram per class, assets tracked, top-N assets by
   risk (`GET /v1/assets`), open alerts per site.
2. **Programme health**: alerts/day, precision proxy, MTTA, feedback
   verdict split, work orders created vs cancelled.
3. **Service**: RED panels, Kafka batch results, in-flight, GC/heap.

## Common failures

**Symptom: an asset never leaves `baseline_source=class`.**
Check `GET /v1/assets/{id}/health`: `window_fill < 1` means a signal in
the template is not being received (wrong signal name or unit). Fix the
gateway mapping or the template.

**Symptom: alert fired but no work order.**
Look at the alert's `work_order_id`. Empty with
`work_orders_total{result="failed"}` rising: CMMS outage, retries run for
`WORKORDER_RETRIES` evaluations. Empty with `result="skipped"`: confidence
below the class `confidence_threshold`, or an open work order already
covers the asset/failure mode (the alert then links to it).

**Symptom: alerts suppressed on an asset that is clearly degrading.**
The asset is inside its post-clear suppression window or hit the per-asset
hourly limit. Health shows `alert_state=active` with no `open_alert_id`.
Either accept (the episode is recorded) or shorten `alerting.suppression`
in the template.

**Symptom: technicians dismiss most alerts for a class.**
Precision proxy low. First check contributors on dismissed alerts: a
single feature (often a temperature `std`) dominating means the class
baseline MAD floor is too small; raise `robust_z.min_mad_abs` or remove
the stat from that signal. Then consider raising `on_threshold` for the
class rather than relying on per-asset offsets.

**Symptom: Kafka lag growing.**
Check `predictmaint_kafka_batches_total{result="error"}`: store errors
fail batches and they are retried after 1 s. If the store is healthy,
scale replicas (HPA) - partitions are the parallelism unit.

## Operations

**Template change.** `PUT /v1/templates/{class}` validates the whole
document and rebuilds assets of that class in place (baselines and offsets
survive, windows restart). Bump `version` so alerts record which recipe
fired.

**Rollback.** Standard Helm rollback. State compatibility: templates and
snapshots are JSON documents with additive fields; an older binary ignores
unknown fields. Alerts and work orders are never rewritten by a deploy.

**Restart.** Assets restore learned baselines and offsets from snapshots.
Each asset needs one full window of samples (256 vibration samples = one
burst; 3 scalar snapshots) before it scores again.

**Scaling.** Replicas scale on CPU/memory via HPA; Kafka partitions bound
useful parallelism. Rule of thumb: 2000 assets per 512 MiB replica.

**Local reproduction.** `make run` in one shell, `make simulate` in
another reproduces the canonical 5-pump scenario in under a second.
