# Releasing

For maintainers. Releases are cut from `main` by pushing a `v*` tag; `.github/workflows/release.yml` does the rest
(tests, reproducible cross-compile, packages for Linux/macOS/Windows, checksums, provenance attestation, GitHub
**pre-release**). Everything is an alpha until the changelog says otherwise.

## Versioning

[Semantic Versioning](https://semver.org/) with pre-release tags while in alpha: `v0.1.0-alpha`, `v0.1.0-alpha.2`,
`v0.2.0-alpha`. While the major version is 0, minor bumps may break CLI flags, profile formats or contracts; the changelog
says so under a **Breaking** note.

## Checklist

1. **Main is green.** CI (all three OSes + packaging smoke), `security` and `codeql` workflows pass on `main`.
2. **Changelog.** Move the contents of `## [Unreleased]` under a new `## [X.Y.Z-alpha] - YYYY-MM-DD` heading, leave an
   empty `[Unreleased]` above it, and update the compare links at the bottom.
3. **Docs and numbers.** Anything in the README that quotes a benchmark was measured on a commit not older than this
   release's behaviour; if code that affects it changed, re-run the benchmark or mark the number as older.
4. **Open a release PR** titled `chore(release): vX.Y.Z-alpha` with only the changelog (and version) change; merge it.
5. **Tag** the merge commit and push the tag:
   ```sh
   git switch main && git pull --ff-only
   git tag -s vX.Y.Z-alpha -m "Sweef X.Y.Z-alpha"    # -s needs a GPG/SSH signing key; use -a if you have none
   git push origin vX.Y.Z-alpha
   ```
6. **Watch the `release` workflow.** It publishes a pre-release with installers and `SHA256SUMS`. Without signing secrets
   the macOS and Windows installers are unsigned and the notes say so (see [install.md](install.md)).
7. **Smoke-test** one installer on a clean machine (Linux is the verified platform), then announce.

## Secrets (all optional; a missing one skips that signing step loudly)

`MACOS_CERT_P12`, `MACOS_CERT_PASSWORD`, `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD`,
`WINDOWS_CERT_PFX`, `WINDOWS_CERT_PASSWORD`, `UPDATE_SIGNING_KEY` (ed25519, see [updates.md](updates.md)).

## Hotfixes

Branch `release/X.Y` from the tag, cherry-pick the fix (`git cherry-pick -x`), update the changelog, tag `vX.Y.(Z+1)`,
then merge the fix and changelog back into `main`.

## Version in the binary

`cmd/sweef` declares `version` and `commit` as variables; the release scripts inject them with `-ldflags -X`, and the build
fails if the built binary does not report the version it was given (`scripts/release/build.sh`).
