# ADR-0004: Detector choice and scaling model (the key trade-off)

- Status: Accepted
- Date: 2026-02-10

## Context

Two decisions shape everything downstream: which detectors run per asset,
and how the fleet is spread across replicas. Both trade detection power
against cost and explainability.

## Decision

### Detectors

Four detectors, all O(features) per evaluation, all with closed-form
scores in [0,1]:

| Detector | Catches | Rule |
|---|---|---|
| Robust z-score (median/MAD, direction-aware) | Sharp univariate departures: RMS, kurtosis, band amplitude | `ZS-1` |
| Half-Space Trees (streaming, 25 trees, depth 5) | Multivariate density drops the z-score misses | `HST-1` |
| Two-sided CUSUM on a standardised key feature | Slow drift below the z-score threshold | `CUSUM-1` |
| Weibull class prior | Age-conditioned failure probability | `WB-1` |

Fusion (`FUSE-1`) blends rather than maxes: `anomaly = 0.65*z + 0.35*hst`,
`risk = 0.6*anomaly + 0.25*drift + 0.15*prior`. The consequences are
deliberate:

- HST alone can contribute at most 0.35 to anomaly. In a sparse
  ~13-dimensional cube with a 64-point reference window, HST occasionally
  isolates a normal point; blending means that cannot fire an alert.
- The prior alone contributes at most 0.15. An ancient pump is watched more
  closely (a smaller anomaly tips it over) but is never alerted for being
  old.
- HST adapts to a persistent new regime (its reference window fills with
  degraded points) while the z-score does not (baselines are frozen after
  warm-up). During a long episode the anomaly score settles around
  0.65-0.7; that is the intended split between "change detector" and
  "reference detector".

Spectral features use Goertzel single-bin estimates rather than an FFT:
templates name the two or three frequencies that matter (1x shaft, BPFO,
valve), and O(N) per band per burst keeps the CPU budget flat.

Rejected alternatives: per-asset autoencoders or LSTMs (unexplainable
alerts, retraining burden, GPU cost); full isolation forests (batch
retraining, memory); raw FFT per burst (unnecessary for named bands).

### Scaling

Assets are keyed in Kafka by `asset_id`; a consumer group with N replicas
splits the keyspace. Each replica holds state only for its assets. Memory
per asset is dominated by HST (25 trees x 63 nodes) and the 256-sample
vibration window: roughly 100-150 KB, so a 512 MiB pod comfortably holds
~2000 assets. The HTTP ingest path is for edge gateways and tests and is
not partition-aware; a fleet deployment ingests through Kafka.

## Consequences

- Alert precision is favoured over recall at the detector layer and
  recall is recovered over time (persistence, drift). This matches the
  domain: bearing wear evolves over days; a missed snapshot costs nothing,
  a false alarm costs trust.
- Site-specific tuning is a template PUT, not a code change.
- Re-sharding (replica count change) re-enters one window of cold start
  for moved assets, with learned baselines restored from snapshots.
