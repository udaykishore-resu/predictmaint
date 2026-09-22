# ADR-0003: Failure handling, idempotency and back-pressure

- Status: Accepted
- Date: 2026-02-02

## Context

The system sits between an unreliable source (a plant's metric stream) and
an unreliable sink (a CMMS with change windows and outages), and its
output is a human's attention. Failures must never produce duplicate work
orders, lost technician feedback or a flood of repeated alerts.

## Decision

**Ingest.** Kafka is consumed at-least-once: offsets are committed only
after `Engine.Ingest` returns without a persistence error. Undecodable
records are counted and skipped so a poison pill cannot wedge a partition.
Per-metric validation failures (bad unit, unknown signal, missing class)
are reported in the result and never fail the batch. Only store errors fail
a batch, and those are safe to replay because of the per-signal high-water
mark.

**Alerting.** An alert is emitted at most once per episode. Nuisance
protection is layered and each layer is a rule with a version:
hysteresis (`on`/`off` thresholds), minimum persistence, clear persistence,
a post-clear suppression window, a per-asset hourly limit and a per-site
hourly limit. Withheld alerts are still recorded as episodes and counted in
`alert_outcomes_total{outcome="suppressed"|"rate_limited"}` so a
misconfigured site is visible rather than silent.

**Work orders.** Created only when fused confidence meets the class
threshold and no open work order exists for the asset and failure mode.
The CMMS call carries an idempotency key `asset|failure_mode|alert`; the
simulated CMMS (and any real adapter) must return the existing reference
for a repeated key. If the CMMS is down the alert is still persisted and
the engine retries on subsequent evaluations of the same active episode,
bounded by `WORKORDER_RETRIES`. Failures are logged with the alert id and
counted in `work_orders_total{result="failed"}`.

**Feedback.** Repeating the same verdict is a no-op; a conflicting verdict
is a 409. Threshold adaptation is clamped to `max_offset` and
`max_threshold` so no amount of dismissals can silence an asset, and no
amount of confirmations can push the threshold into the hysteresis band.

**Shutdown.** SIGTERM stops the Kafka poll loop, drains HTTP for
`SHUTDOWN_TIMEOUT`, flushes traces and closes the store. In-flight batches
complete; uncommitted ones are redelivered.

## Consequences

- Duplicate alerts or work orders require two independent failures (engine
  dedupe *and* the database's partial unique index).
- Recovery after a CMMS outage is automatic and bounded; operators see it
  in metrics rather than discovering unlinked alerts later.
- The trade-off is latency: `min_persistence` evaluations at the snapshot
  cadence before an alert. For a five-minute cadence and persistence 3
  that is 15 minutes, which is far inside the days-to-weeks lead time of
  bearing wear.
