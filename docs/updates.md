# Auto-update design

Status: **design plus a tested verification library** (`internal/update`). It is deliberately not wired into the
CLI or the GUI yet. Alpha builds do not update themselves.

## Goals

1. An attacker who controls the download host, a CDN, a mirror or the user's network must not be able to make
   Sweef run code we did not sign.
2. A failed or interrupted update must never leave the user without a working browser.
3. No background service, no admin rights: update the files the user owns.
4. Boring and auditable: standard library only, one JSON document, one signature.

Non-goals for v1: delta updates, silent updates of system-wide (root-owned) installs, package-manager installs.
Installs done with `apt`/`dnf`/winget/Scoop/Homebrew are updated by those tools; the updater only *tells* the
user (it detects a root-owned or package-managed binary and refuses to replace it).

## Trust model

- One **ed25519 signing key**, held only as the `UPDATE_SIGNING_KEY` CI secret (generate with
  `go run ./packaging/tools/pkgtool keygen`). The **public** key (or keys) is compiled into the binary.
- The signature covers the *exact bytes* of `latest.json`; there is no canonicalisation step to get wrong.
  `latest.json.sig` is the base64 detached signature.
- TLS protects privacy, not authenticity: a valid TLS connection to a compromised host proves nothing, so the
  signature is the only thing trusted.
- **Key rotation:** the binary carries a list of public keys; a manifest verifies if *any* matches. Ship the new
  key in release N while still signing with the old key, then switch signing in release N+1.
- **Key compromise** is the hard case: users on old builds only trust old keys. Mitigation is to keep the key out
  of reach (CI secret, ideally an environment with required reviewers) and to rotate on schedule. Hardware-backed
  signing is a later hardening step.
- This is separate from OS code signing (Authenticode / Developer ID): those make the *first* install trusted by
  the OS; the ed25519 signature authenticates *updates* even for unsigned alpha builds.

## The manifest (`latest.json`)

```json
{
  "schema": 1,
  "channel": "alpha",
  "version": "0.1.0-alpha.7",
  "published": "2026-10-12T11:40:02Z",
  "expires": "2026-11-26T11:40:02Z",
  "notes_url": "https://github.com/sweefbrowser/sweef/releases/tag/v0.1.0-alpha.7",
  "artifacts": [
    {"os": "linux", "arch": "amd64", "kind": "archive",
     "url": "https://github.com/sweefbrowser/sweef/releases/download/v0.1.0-alpha.7/sweef_0.1.0-alpha.7_linux_amd64.tar.gz",
     "sha256": "<64 lowercase hex>", "size": 22000000}
  ]
}
```

- `expires` stops a **freeze attack** (an attacker replaying an old, validly signed manifest forever to hide a
  security fix). The release job sets 45 days; the client rejects expired manifests.
- Rollback attack: the client only installs a version **strictly newer** than the running one (semver ordering,
  pre-releases before the release).
- `sha256` and `size` are inside the signed document, so the download needs no separate trust: the client streams
  the file, never writes more than `size` bytes, and compares the hash before touching the install.
- Unknown JSON fields are rejected so a future schema is not half-understood by an old client.
- Published at a stable URL: the rolling `updates` pre-release, `.../releases/download/updates/latest.json`
  (+ `.sig`). The release workflow overwrites those two assets after each release.

## Client flow

1. **Check** (at most once a day, and on demand): fetch `latest.json` and `latest.json.sig` (1 MiB cap each),
   `Verify` against the embedded keys, `Parse`, check `expires`, check "newer". Any failure means "no update",
   logged, never fatal.
2. **Choose the artifact** for `runtime.GOOS/GOARCH` and the kind the platform can swap in place:
   Linux and Windows: the raw binary or the portable archive; macOS: the `.app` zip (`universal`).
3. **Download** to `<dir>/.sweef-update/<name>.part` with HTTP `Range` resume, then size and SHA-256 check, then
   rename. A short read keeps the partial file for the next attempt; corrupt or oversized data deletes it.
4. **Stage** next to the install: write `sweef.new` on the same filesystem (so the swap is a rename), mode copied.
5. **Swap** (`update.Replace`): `sweef` becomes `sweef.old`, then `sweef.new` becomes `sweef`. Renaming a running
   executable is allowed on Linux, macOS and Windows; each rename is atomic. If the second rename fails the old
   binary is put back immediately.
6. **Restart** into the new binary with a `--post-update` marker. The new process runs a self-check
   (`version` matches, engine initialises, config loads). On success it calls `Commit` (deletes `sweef.old`).
   On failure, or if it crashes before committing (detected by the marker still being present on next start), the
   launcher calls `Rollback` and the old binary returns.
7. **Crash safety:** `update.Recover` runs at every start. If power was lost between the two renames, `sweef` is
   missing but `sweef.old` exists and is restored; stray `.new` and `.bad` files are cleaned.

On macOS the whole `Sweef.app` is staged as a sibling directory and swapped with `renamex_np(RENAME_SWAP)` (same
shape: stage, swap, health check, rollback). The bundle signature check by Gatekeeper still applies after the
swap, which is why signed and notarized releases matter for macOS updates.

## What the library covers (`internal/update`)

| Function | Purpose |
|---|---|
| `ParsePublicKey`, `Sign`, `Verify`, `VerifyAndParse` | ed25519 over exact bytes, multi-key, expiry |
| `Parse`, `Manifest.Validate`, `Manifest.Select` | strict schema, https/http URLs only, lowercase hex hashes, platform pick |
| `ParseVersion`, `IsNewer` | semver precedence; unparseable never counts as newer (fail closed) |
| `Fetch`, `Check` | size-capped manifest fetch, verify, newer-only |
| `Download` | resumable, size-limited, SHA-256 verified, deletes corrupt data |
| `Replace`, `Commit`, `Rollback`, `Recover` | staged rename swap with rollback and power-loss recovery |

All of it is covered by unit tests using `net/http/httptest` and temp directories (`go test ./internal/update`).
`pkgtool manifest` (used by the release workflow) writes and signs the manifest using the same types, and
`packaging/tools/pkgtool` has a test that signs a manifest and verifies it with this package.

## Still to do before enabling

- Wire `Check` into `sweef gui` (settings: channel, "check automatically", "check now") and a `sweef update` CLI.
- Embed the public key(s); decide where the key list lives (`internal/update/keys.go`, set at release).
- The restart handshake and health check (step 6), the macOS bundle swap, and Windows handling of a locked
  `sweef.old` (it is cleaned by `Recover` on the next start).
- Detect package-managed and root-owned installs and switch to "tell the user".
- A staged rollout field (`rollout_percent` keyed on a stable per-install random id) and a revocation list for
  yanked versions, if we ever need to pull a bad release.
- Optional: a second signature from a second key (threshold signing) so one leaked CI secret is not enough.
