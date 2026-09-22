# Security Policy

## Supported versions

Only the latest tagged release and `main` receive security fixes.

## Reporting a vulnerability

Please do not open public issues for security problems. Email the maintainer
(see `CODEOWNERS`) or use GitHub's private vulnerability reporting on this
repository. You will get an acknowledgement within 72 hours and a fix or
mitigation plan within 14 days for confirmed issues.

## Design notes relevant to security

- The service has no authentication of its own. It is designed to run behind
  a service mesh or gateway that terminates TLS and enforces identity; the
  Helm chart's `NetworkPolicy` restricts ingress to the namespace's gateway.
- All request bodies are size-capped (`HTTP_MAX_BODY_BYTES`) and decoded
  with unknown-field rejection where the schema is fixed.
- Identifiers (`asset_id`, `site`) are validated against a conservative
  character set before they reach storage or logs.
- SQL is parameterised; column names come from a fixed allow-list.
- The container runs as a non-root distroless image with a read-only root
  filesystem and all capabilities dropped.
- Secrets (database DSN, broker credentials) are read from environment
  variables that the chart sources from an external `Secret`; nothing
  sensitive is committed.
