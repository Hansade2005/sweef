# Contributing to Sweef

Thank you for helping. Sweef is a from-scratch browser engine and desktop browser in pure Go (no CGO), built to be
fast, light and agent-first. This page tells you how to get a change from idea to merged.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems go through
[SECURITY.md](SECURITY.md), never through a public issue.

## Ground rules

1. **Evidence over claims.** Performance, memory, compatibility and data-saving statements must come from a
   measurement that can be re-run (`bench/`, `internal/app/cmd/appbench`, `fidelity/`). Say what was measured, on
   what, and what it does *not* show. A number without a method is not accepted into the README or the docs.
2. **Pure Go.** `CGO_ENABLED=0` must keep working on Linux, macOS and Windows. Do not add a dependency that needs cgo.
   New third-party modules need a reason in the PR and a compatible licence (Apache-2.0, BSD, MIT, ISC or similar).
3. **Honest limits.** If something is untested on a platform, the docs say so. Do not widen a claim to fit a result.
4. **Small, reviewable changes.** One concern per PR. Refactors and behaviour changes go in separate PRs.

## Getting set up

You need Go (the version in `go.mod`) and git. Nothing else is required to build and run most tests.

```sh
git clone https://github.com/sweefbrowser/sweef.git
cd sweef
make check          # gofmt, go vet, short tests: what CI runs
make run            # build and open the desktop browser (Linux/X11 is the verified platform)
```

Some tests compare rendering against Chromium reference captures and need the reference font set that CI installs
(see the "Install the reference fonts" step in `.github/workflows/ci.yml`). Without those fonts, fidelity, render, raster
and font tests can fail on your machine for reasons that are not your change; CI is the judge for those. Tests that
need ffmpeg or Xvfb skip themselves when it is missing.

Useful commands:

| Command | What it does |
|---|---|
| `make build` | build `./sweef` |
| `make test` | short tests, all packages |
| `make test-all` | everything, including the slow FATE-gated tests |
| `make fmt` / `make fmt-check` | format / verify formatting (`third_party/` is excluded) |
| `make bench-gui` | the in-process GUI latency report |
| `make embeds` | rebuild the compressed embeds after editing JavaScript under `internal/engine`, a file under `internal/ui/web`, or a font; the tests fail with this instruction when they are stale |
| `make size` | size, headroom to the 40 MiB budget and bytes per package of the release binary (`docs/development.md`, "Binary size budget") |
| `go run ./internal/app/cmd/appbench -open-profile out.prof` | CPU profile of the key that opens the omnibox dropdown |
| `SWEEF_LAYOUT_CHECK=1`, `SWEEF_INCR_CHECK=1` | debug modes that compare cached/incremental layout and paint with a from-scratch one |

Read [docs/development.md](docs/development.md) for the package map and the contracts between the chrome (HTML/JS drawn by
our own engine), the Go app and the native window shells.

## Making a change

1. **Discuss first for anything big.** Open an issue (or a discussion) for new features, new dependencies, anything that
   changes a documented contract (`docs/gui-contract.md`, `docs/data-saver.md`) or the architecture. Bug fixes and
   small improvements can go straight to a PR.
2. **Branch** from `main`: `feat/<topic>`, `fix/<topic>`, `perf/<topic>`, `docs/<topic>`.
3. **Write the change with its test.** Bug fixes come with a test that fails without the fix. Rendering changes keep the
   paint goldens and the fidelity suite unchanged, or explain in the PR why a golden moved and regenerate it in a clean
   checkout (`git worktree`) so a half-edited tree cannot leak into it.
4. **Run `make check`** before pushing. Run the package tests for what you touched without `-short` too.
5. **Update the docs and the changelog** (see below).
6. **Open the PR** against `main` using the template. Keep the title in Conventional Commit form (see below), because
   it becomes the squash/merge title and feeds the release notes.

### Commit and PR titles: Conventional Commits

```
<type>(<scope>): <summary in the imperative, no full stop>
```

Types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `revert`.
Scopes (pick the closest): `engine`, `render`, `raster`, `style`, `js`, `net`, `media`, `gl`, `pdf`, `font`, `app`, `ui`,
`shell`, `agent`, `mcp`, `datasaver`, `cli`, `release`, `docs`.
Breaking changes add `!` (`feat(cli)!: ...`) and a `BREAKING CHANGE:` paragraph in the body.

### Sign-off (DCO)

Every commit carries `Signed-off-by: Your Name <you@example.com>` (use `git commit -s`). It certifies the
[Developer Certificate of Origin](https://developercertificate.org/): you wrote the change or have the right to submit it
under the project licence (Apache-2.0).

### Changelog

User-visible changes add one line under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md), in the right section
(`Added`, `Changed`, `Fixed`, `Performance`, `Security`, `Removed`). Pure refactors, tests and CI changes do not need an entry.

### What a good PR contains

* What and why, in a few sentences. Link the issue.
* How you tested it, and on which OS. Say what you could **not** test (macOS and Windows windows have never been run
  by the maintainers; if you can run them, that is valuable information).
* For performance work: before/after numbers from the same machine and method, with the command to reproduce.
* For UI: screenshots for light and dark themes, and at 1x and 2x scale where layout changes.

## Review

A maintainer reviews every PR (see [GOVERNANCE.md](GOVERNANCE.md)). CI must be green: build, vet and tests on Linux,
macOS and Windows, plus the Linux packaging smoke test. We aim to give a first response within a week. Reviewers
comment on the code, not the person. If a review stalls, ping the PR once after a week.

## Reporting bugs and asking for features

Use the issue forms. A good bug report has the Sweef version (`sweef version`), OS, the URL or a minimal page, what you
expected and what happened, and for rendering bugs a screenshot of Chromium next to ours. For performance regressions,
use the performance form and include the command and numbers.

## Licence

Contributions are licensed under the [Apache License 2.0](LICENSE).
