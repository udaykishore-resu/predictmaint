# ADR-0001: Asset-class templates with a deterministic per-asset pipeline

- Status: Accepted
- Date: 2026-01-15

## Context

Predictive-maintenance pilots routinely succeed on ten machines and die at
a thousand. The two recurring causes are (1) a model per machine, which
nobody can retrain, validate or explain at fleet scale, and (2) nuisance
alerts that make technicians stop reading. We need a design whose cost
grows with the number of *asset classes*, not the number of assets, and
whose every alert can be explained by a rule a reliability engineer can
read.

## Decision

1. **Templates own hyperparameters; assets own baselines.** A template per
   class (`pump`, `motor`, `compressor`, ...) fixes the signals, feature
   recipe, detector hyperparameters, Weibull prior, fusion weights, alert
   policy and work-order policy. An asset learns only a robust baseline
   (median/MAD per feature) during a warm-up window and a bounded threshold
   offset from technician feedback. Cold start transfers the class
   baselines, so a freshly commissioned pump is monitored from its first
   snapshot.
2. **Deterministic core, ML confined to scoring.** Feature extraction,
   fusion, alert policy and work-order decisions are closed-form rules
   with versions (`ZS-1`, `HST-1`, `CUSUM-1`, `WB-1`, `FUSE-1`, `ALERT-1`,
   `ADAPT-1`, `WO-1`) logged with every decision. The only "learned"
   components are the robust baselines and Half-Space Trees masses, both of
   which are inspectable numbers.
3. **Pure domain, ports for I/O.** `internal/domain/*` has no I/O and no
   third-party dependencies. Kafka, PostgreSQL and the CMMS sit behind
   `internal/ports` with an in-memory implementation so the whole system
   runs and is tested without infrastructure.
4. **Per-asset state lives in memory, durable facts in the store.**
   Windows and detector masses are rebuilt from the stream; alerts, work
   orders, templates, learned baselines and threshold offsets are persisted
   (see ADR-0002).

## Consequences

- Adding a machine costs nothing beyond its metrics; adding a class costs
  one template review.
- Every alert carries its contributors, decision reason and rule versions,
  so "why did this fire?" is answerable from the record alone.
- Detector sophistication is deliberately capped: no per-asset neural
  models. Where a site needs one, it can be added as a further detector
  behind the same fusion rule without changing the policy layer.
- A replica holds state for the assets it consumes; horizontal scale comes
  from Kafka partitioning by asset key (ADR-0004).
