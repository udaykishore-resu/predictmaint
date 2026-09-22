# Contributing

Thanks for taking the time. This repository favours small, explicit Go and
recorded decisions over cleverness.

## Ground rules

- Go 1.26+. `make test vet lint` must pass before you open a pull request.
- The domain packages under `internal/domain` are pure: no I/O, no clocks
  without injection, no third-party dependencies beyond the standard library.
  Anything that talks to the outside world goes behind an interface in
  `internal/ports` with a real adapter and an in-memory adapter.
- Every rule that can fire on the hot path carries a rule version constant
  (`ZS-1`, `ALERT-1`, `WO-1`, ...). Bump the version when you change the
  behaviour, and mention it in the changelog section of your PR.
- Non-trivial design changes get an ADR in `docs/adr/` (copy the shape of an
  existing one). Small fixes do not.
- Tests: table-driven unit tests for logic, and keep
  `internal/domain/engine/engine_test.go`'s end-to-end scenario green. If you
  change detector defaults, re-run `make simulate` against `make run` and
  confirm the demo still alerts exactly once on `pump-003`.

## Workflow

1. Fork and branch from `main`.
2. Make your change with tests.
3. `make tidy test vet lint`.
4. Open a PR describing the *why*; link the ADR if there is one.

## Commit messages

Conventional-ish: `engine: retry work order creation after CMMS outage`.
Body explains the reasoning, not the diff.
