# 0001. License the project under Apache-2.0

* Status: Accepted
* Date: 2026-10-13
* Deciders: @Hansade2005

## Context
The README said "MIT (TBD)". A browser engine is patent-dense territory, it bundles and depends on code under BSD, MIT and
Apache-2.0 (QuickJS bindings, purego, media code), and we want companies to be able to adopt and contribute to it.

## Decision
Apache License 2.0 for all first-party code (`LICENSE`, `NOTICE`). Contributions are accepted under the same licence with a
DCO sign-off. Third-party code keeps its own licence (`third_party/`, Go modules).

## Alternatives considered
* **MIT / BSD-3:** simpler, but no explicit patent grant or patent-retaliation clause.
* **MPL-2.0:** file-level copyleft; would make embedding in proprietary products harder for no clear gain.
* **GPL/AGPL:** would block most commercial embedding and conflict with the goal of a widely usable engine.

## Consequences
New dependencies must be Apache-2.0, BSD, MIT, ISC or similarly permissive (enforced by the dependency review workflow).
Changing the licence later needs agreement of all contributors, which is why the choice is recorded now.
