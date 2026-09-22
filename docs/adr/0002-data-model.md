# ADR-0002: Data model and persistence boundaries

- Status: Accepted
- Date: 2026-01-20

## Context

The hot path evaluates every asset on every snapshot. Persisting rolling
windows and detector state per evaluation would make the store the
bottleneck and buy little: that state is reconstructible from the stream
within one window. What is *not* reconstructible is what humans did and
what we told other systems.

## Decision

Persist four kinds of records, all as JSONB documents with the filterable
columns promoted, all upserted by primary key so any write can be replayed:

| Record | Key | Why it must survive a restart |
|---|---|---|
| `alerts` | alert id | Technician feedback, MTTA, precision proxy |
| `work_orders` | work order id (+ unique open `(asset, failure_mode)`) | Dedupe against the CMMS; audit |
| `templates` | class | Operator tuning is authoritative over built-in defaults |
| `asset_snapshots` | asset id | Learned baselines and threshold offsets: without them a restart re-enters cold start for the whole fleet |

Rolling windows, Half-Space Tree masses, CUSUM statistics and alert
episode state are **not** persisted. After a restart an asset resumes with
its learned baselines and offset, and needs one window of samples to score
again.

Metric ingest carries an idempotency mechanism that needs no storage: per
asset and signal the engine keeps the newest accepted event timestamp and
treats anything not newer as a duplicate. Kafka redelivery and HTTP
retries are therefore harmless.

The PostgreSQL adapter enforces one open work order per asset and failure
mode with a partial unique index, so the engine's dedupe has a database
backstop.

## Consequences

- Store writes happen only on alert fire/clear, work order create/cancel,
  template change, baseline fit and feedback. At 10k assets evaluated every
  five minutes the store sees tens of writes per hour, not thousands per
  second.
- JSONB keeps the schema stable while the domain types evolve; the promoted
  columns are the only thing queries depend on.
- A moved asset (partition rebalance) needs one window to score again;
  this is visible as `window_fill < 1` in the health endpoint and is the
  accepted cost of not persisting windows.
