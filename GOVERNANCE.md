# Governance

Sweef is an open source project started and led by its creator. This page says how decisions are made so contributors
know what to expect. It will change as the project grows.

## Roles

* **Maintainers** merge pull requests, cut releases, triage issues and enforce the [Code of Conduct](CODE_OF_CONDUCT.md).
  Current maintainers: [@Hansade2005](https://github.com/Hansade2005) (project lead).
* **Contributors** are everyone who opens an issue, reviews, or sends a change.

Becoming a maintainer: sustained, high-quality contributions (code, review, triage) over several months, then an invitation
from the existing maintainers. Maintainers who are inactive for a year are moved to emeritus.

## How decisions are made

* Day to day: a pull request is merged when a maintainer approves it and CI is green. Reviews aim for consensus.
* Larger decisions (architecture, new dependencies, public contracts, licence, release policy) are proposed as an issue or
  an **ADR** in [`docs/adr/`](docs/adr/README.md) and discussed in the open. If consensus does not emerge, the project
  lead decides and records the reason.
* Security and conduct matters are handled privately ([SECURITY.md](SECURITY.md)).

## Principles that decide close calls

1. Measured claims only (see "Ground rules" in [CONTRIBUTING.md](CONTRIBUTING.md)).
2. Pure Go and a small dependency set.
3. User privacy and data thrift by default; no project-owned API keys, no telemetry without explicit opt-in.
4. Fast and light beats feature-rich.

## Releases

Maintainers cut releases from `main` following [docs/releasing.md](docs/releasing.md). All releases are pre-releases
("alpha") until the project lead declares otherwise in the changelog.

## Licence and contributions

Code is under the [Apache License 2.0](LICENSE). Contributors keep their copyright and certify their commits with a DCO
sign-off (see CONTRIBUTING.md).
