<!-- Title: Conventional Commit form, e.g. `perf(ui): reuse omnibox rows`. See CONTRIBUTING.md. -->

## What and why
<!-- A few sentences. Link the issue: Fixes #123 -->

## How it was tested
<!-- Commands you ran and on which OS. Say what you could NOT test (macOS/Windows windows, real GPUs, ...). -->

## Evidence (performance, memory, data, compatibility claims only)
<!-- Before/after numbers, same machine and method, with the command to reproduce. Delete this section if not applicable. -->

## Screenshots (UI changes)
<!-- Light and dark; 1x and 2x if layout changed. -->

## Checklist
- [ ] `make check` passes locally (gofmt, vet, short tests)
- [ ] Tests added or updated (a bug fix has a test that fails without it)
- [ ] `CHANGELOG.md` updated under `[Unreleased]` (user-visible changes)
- [ ] Docs updated (README, `docs/`, contracts) where behaviour changed
- [ ] No new cgo; new dependencies justified and licence-compatible
- [ ] Commits are signed off (`git commit -s`)
