# DRM key systems (CDM backends)

Sweef plays protected video the way other independent browsers and apps do: by **hosting the vendor's own, licensed
Content Decryption Module (CDM) that the user already has**. Sweef does not implement, reimplement or circumvent any
DRM scheme, and it does not bundle, download or redistribute a CDM. If no CDM is found, the key system reports itself
unsupported and sites fall back to something else (or say they cannot play).

This page is the contract for the Encrypted Media Extensions (EME) layer, the operating notes for users, and an honest
list of what has and has not been tested. Code: `internal/media/cdm` (package docs there), `cmd/sweef-cdm`.

## What works, what is untested

| Key system | Backend | Status |
|---|---|---|
| `org.w3.clearkey` | in-process, `cdm.ClearKey` | **Implemented and tested** (cenc, cens, cbc1, cbcs decryption checked against independent encryptors; license and init data validation). |
| `com.widevine.alpha` | the user's `libwidevinecdm` in the helper process `sweef-cdm`, hosted through the Chromium CDM C++ ABI with purego | Discovery, the framed protocol, the registry and capability negotiation, and the **whole host** (vtables, callbacks, buffers, timers, file IO, promises) are tested against a fake CDM that implements the same binary interface in Go. **Never run against a real Widevine library**: none exists in the sandbox it was written in, and none may be bundled. |
| `com.microsoft.playready`, `.recommendation` | Windows' PlayReady through Media Foundation, COM vtables | **UNVERIFIED, compile-checked only.** License exchange only; playback handoff is not implemented. Off unless `SWEEF_PLAYREADY=1`. |
| `com.apple.fps` | AVFoundation `AVContentKeySession` through purego's Objective-C runtime | **UNVERIFIED, compile-checked only.** License exchange only; playback handoff is not implemented. Off unless `SWEEF_FAIRPLAY=1`. |

Things that touch a real Widevine, PlayReady or FairPlay component and therefore cannot be called working: loading the
library, `VerifyCdmHost_0` and what the CDM does with host files and signatures, device provisioning
(individualization), threading behaviour of the real CDM, persistent licenses, every real license server, hardware
robustness. See "Verification" below for exactly what the tests cover.

## Architecture

```
 page JS  ->  EME layer (navigator.requestMediaKeySystemAccess, MediaKeys, MediaKeySession)     [other worktree]
                 |   Registry.Negotiate -> Provider.New -> Backend / Session            internal/media/cdm
        +--------+-----------------+----------------------+------------------------+
        |                          |                      |                        |
   ClearKey (in process)     Widevine = Remote       PlayReady (Windows)      FairPlay (macOS)
   cdm.ClearKey              framed protocol over    Media Foundation CDM     AVContentKeySession
                             stdin/stdout            COM vtables, x/sys       purego + objc
                                  |
                          cmd/sweef-cdm  (separate, dynamically linked process)
                                  |  cdm.Serve(Engine)
                          internal/media/cdm/widevinehost   purego dlopen + C++ vtables
                                  |
                          libwidevinecdm.so / widevinecdm.dll / libwidevinecdm.dylib   (the user's Chrome copy)
```

Why a helper process: the Linux release binary of Sweef is fully static (no libc, no dynamic loader; it runs on Alpine
and in `scratch` containers), and loading a shared library needs the dynamic loader. The helper is the one binary that is
allowed to be dynamically linked (purego's Linux shim imports `dlopen`). It also keeps a crashing or misbehaving
proprietary library out of the browser process. Everything the browser imports (`internal/media/cdm`,
`.../platform`) is pure Go; a test (`TestKeySystemLayerStaysFreeOfDlopen`) fails if purego ever leaks into that import
graph. The helper is built next to the browser by `scripts/release/build.sh` (`--skip-cdm-host` to omit it); packaging
(deb, AppImage, dmg, MSI) does not install it yet.

## The Go interface the EME layer plugs into

```go
reg := platform.NewRegistry(platform.Options{WidevinePath: settings.WidevinePath})   // Clear Key + the rest

// requestMediaKeySystemAccess(keySystem, configs)
acc, err := reg.Negotiate(keySystem, configs)          // err -> NotSupportedError
// acc.Config is getConfiguration(); acc.Provider is the key system

// createMediaKeys()
be, err := acc.Provider.New(ctx, acc.NewInstanceConfig(origin, storageDir))   // cdm.Backend

// setServerCertificate(cert)
ok, err := be.SetServerCertificate(ctx, cert)          // ok=false -> resolve(false)

// createSession(type) ; session.generateRequest(initDataType, initData)
s, err := be.CreateSession(ctx, cdm.SessionTemporary)
msg, err := s.GenerateRequest(ctx, "cenc", initData)   // msg.Type, msg.Data -> dispatch a "message" event
go func() { for ev := range s.Events() { ... } }()     // renewals, keystatuseschange, expiration, closed

// session.update(response)
err = s.Update(ctx, licenseBytes)

// media pipeline, per encrypted sample (cenc/cbcs auxiliary info already parsed)
clear, err := be.Decrypt(ctx, &cdm.Sample{Data: d, KeyID: kid, IV: iv, Subsamples: subs, Scheme: cdm.SchemeCbcs, Pattern: cdm.Pattern{1, 9}})
```

Interfaces (full docs in `internal/media/cdm/cdm.go`, `registry.go`):

- `Registry`: `Register`, `RegisterAlias`, `Lookup`, `KeySystems` (available ones), `Report` (every key system with
  availability and the reason when unavailable: feed this to a diagnostics page), `Negotiate`.
- `Provider`: `KeySystem`, `Availability() {Supported, Reason}` (cheap, never starts a CDM), `Capabilities()`, `New`.
- `Capabilities`: init data types, session types (`SupportsPersistentLicense`), video and audio codec families
  (`CodecFamily` normalizes RFC 6381 strings: avc1, hevc, vp8, vp9, av01, mp4a, opus, vorbis, flac, ac-3, ec-3), containers,
  encryption schemes, robustness levels (`RobustnessLevel{Name, Hardware, MaxHeight}`), distinctive identifier and
  persistent state requirements, `Mode`, CDM version, `Unverified`.
- `Negotiate` implements the "get supported configuration" steps: init data filtering, `distinctiveIdentifier` /
  `persistentState` resolution, session types, per-capability MIME/codec/robustness/scheme matching, and reports
  `HardwareSecure` when an accepted capability needs a hardware CDM. Capability lists describe what the CDM can
  *decrypt*; `canPlayType`/`isTypeSupported` for the stream itself is still the media pipeline's answer.
- `Backend` (a `MediaKeys`): `SetServerCertificate`, `CreateSession`, `Decrypt`, `Close`, `Mode`, `KeySystem`.
- `Session` (a `MediaKeySession`): `ID`, `Events`, `GenerateRequest`, `Load`, `Update`, `Close`, `Remove`, `KeyStatuses`,
  `Expiration`.
- `Event`: `EventMessage`, `EventKeyStatusesChange` (complete current list + `AdditionalUsableKey`), `EventExpirationChange`
  (zero time = NaN), `EventClosed`. Events are queued without bound and never block the CDM. The first license request is
  *returned* by `GenerateRequest`, later messages arrive as events. **Ordering guarantee:** key status and expiration
  changes caused by `Update` are queued before `Update` returns, so the EME layer can fire `keystatuseschange` first and
  then resolve the promise, as the spec requires.
- Errors: `*cdm.Error{Exception, SystemCode, Message}` maps to the DOMException of the same name (`TypeError`,
  `NotSupportedError`, `InvalidStateError`, `QuotaExceededError`). `ErrNoKey` (no key for this sample yet: wait for a
  key status change and retry), `ErrDecrypt`, `ErrUnsupported`, `ErrClosed`, `ErrHelperDied` (the helper process exited:
  every session is closed with an `EventClosed`; create a new `MediaKeys`).
- `individualization-request`: a Widevine CDM without a device certificate may answer `GenerateRequest` with a message of
  type `individualization-request`. Dispatch it to the page like any other `message` event (this is the EME spec and
  what Chromium does); the page's `update()` carries the answer back and the real `license-request` follows as an event.
- Decryption modes (`Backend.Mode`): `ModeDecrypt` (Clear Key, Widevine here: returns clear compressed samples that
  Sweef's own decoders decode), `ModeDecryptAndDecode` (optional `cdm.Decoder`: `InitVideoDecoder`,
  `DecryptAndDecodeVideo`, ... for CDMs that only decode internally; **no backend implements it yet**, the protocol v1
  has no decoder frames), `ModePlatformRender` (PlayReady, FairPlay: the key never leaves the OS, `Decrypt` returns
  `ErrUnsupported`, and the protected stream must be handed to the platform renderer through the optional
  `cdm.PlatformRenderer`, `StreamHandoff{URL, NativeLayer}`; for FairPlay that means an `AVSampleBufferDisplayLayer` /
  `AVSampleBufferAudioRenderer` added as content key recipients, for PlayReady a Media Foundation Media Engine. **The
  handoff itself is not implemented**; the interface exists so the EME and media layers can be designed around it.)
- Schemes: `cenc`, `cens`, `cbc1`, `cbcs`. The Widevine CDM API only has `cenc` and `cbcs`; `cens`/`cbc1` samples get
  `ErrUnsupported` there. `cdm.DecryptSample(key, sample)` is the reference implementation EME can reuse. For `cens` and
  `cbc1` the treatment of a trailing partial block (left clear) follows ISO/IEC 23001-7 as understood here and is not
  checked against real content; no shipping CDM uses them.
- Clear Key is deliberately the same interface as everything else. If the EME worktree implements its own Clear Key,
  routing it through `cdm.NewClearKey()` removes the duplicate.

## Widevine

### Enabling it

1. Have a Widevine CDM on the machine. Sweef never downloads one. The usual way: install Google Chrome once and let it
   update itself (it keeps the CDM in its profile).
2. Put `sweef-cdm` next to `sweef` (or set `SWEEF_CDM_HELPER=/path/to/sweef-cdm`). Release builds from `build.sh` produce
   it.
3. Discovery order (first hit wins; an explicit setting that is wrong is an error, never a silent fallback):
   1. the setting passed as `platform.Options.WidevinePath` (a library file or a directory),
   2. `SWEEF_WIDEVINE_CDM=/path/to/libwidevinecdm.so` (file, version directory or a `WidevineCdm` root),
   3. Chrome (then Chromium, then Edge) component directories, highest version that has a library for this platform:
      - Linux: `~/.config/google-chrome/WidevineCdm/<ver>/_platform_specific/linux_x64/libwidevinecdm.so` (also
        `google-chrome-beta`, `google-chrome-unstable`, `chromium`, `microsoft-edge`, and `/opt/google/chrome*/WidevineCdm`);
      - Windows: `%LOCALAPPDATA%\Google\Chrome\User Data\WidevineCdm\<ver>\_platform_specific\win_x64\widevinecdm.dll`
        and `%ProgramFiles%\Google\Chrome\Application\<chrome ver>\WidevineCdm\...`;
      - macOS: `~/Library/Application Support/Google/Chrome/WidevineCdm/<ver>/_platform_specific/mac_arm64/libwidevinecdm.dylib`
        (`mac_x64` on Intel) and the Chrome app bundle's `Libraries/WidevineCdm`.

   The `manifest.json` next to the library supplies the version, the codec list (`x-cdm-codecs`), persistent license
   support, supported encryption schemes and the CDM interface versions (`x-cdm-interface-versions`).
4. Check it: `sweef-cdm -probe -cdm /path/to/libwidevinecdm.so` loads the library, calls `VerifyCdmHost_0` and prints the
   CDM version and the interface version it accepted. (No such run has happened in development; see above.)

If nothing is found `Provider.Availability()` says so (`no Widevine CDM found: install Google Chrome once ... or set
SWEEF_WIDEVINE_CDM`), and `requestMediaKeySystemAccess` rejects.

### How the host works

`internal/media/cdm/widevinehost` implements Chromium's CDM host ABI (the `chromium/cdm` repository's
`content_decryption_module.h`, module version 4, interface versions 10, 11, 12; `content_decryption_module_ext.h` for
`VerifyCdmHost_0`). The package comment explains the C++ ABI details; `abi.go` pins every struct size, field offset and
vtable slot with the header's numbers. In short: the library's exports are `InitializeCdmModule_4`, `CreateCdmInstance`,
`GetCdmVersion` (and optionally `DeinitializeCdmModule`, `VerifyCdmHost_0`); `CreateCdmInstance` is handed a
`GetCdmHostFunc` that returns our `cdm::Host_N*`, a pointer to a vtable of purego callbacks; the CDM object we get back
is called through its vtable.

- All calls into the CDM happen on one locked OS thread, as in Chromium; a call that waits for a promise keeps serving
  queued jobs (timers, file IO completions) so it cannot starve the thing it waits for.
- Host services: `Allocate` (buffers), `SetTimer`/`TimerExpired`, `GetCurrentWallTime` (a double return: a struct return
  on Linux/macOS, generated machine code on Windows, see `walltime_windows.go`), per-origin `FileIO` with
  exclusive opens, atomic writes and name validation, and the promise and session callbacks.
- Answers that are honest rather than convenient: `SendPlatformChallenge` fails (no platform attestation),
  `QueryOutputProtectionStatus` reports an unknown link with **no** protection, `RequestStorageId` reports none available.
- Storage: with a storage directory the CDM gets a per-origin directory (`<storage>/widevine/<hash of origin>/`), which is
  where Widevine keeps its device certificate. As far as is publicly visible, Chrome grants the desktop Widevine CDM this storage without a per-site prompt; so does Sweef. Without a
  storage directory `CreateFileIO` returns NULL.
- Not provided: a sandbox. Chrome runs the CDM in a sandboxed process; `sweef-cdm` runs with the user's rights. Treat the
  CDM as the closed-source code it is. A seccomp/landlock/AppContainer profile for the helper is future work.
- Decrypt-only: the desktop CDM is used to decrypt, Sweef decodes. No decoder frames cross the protocol.

### The 720p limitation

The desktop Widevine CDM Chrome ships is a software CDM: Widevine security level **L3** (`SW_SECURE_CRYPTO`). Services
set their own policy for L3 and most cap it at 720p or lower (some at 540p/480p), regardless of the display, and HDR/4K
and "HD" tiers on some services need hardware (L1) which a software CDM cannot provide. This is the license servers'
policy, not something Sweef or the CDM enforces; `Capabilities.Robustness` records `SW_SECURE_CRYPTO` with
`MaxHeight: 720` as the typical ceiling, and the software CDM does not advertise `HW_SECURE_*` robustness.

### Verified Media Path (VMP) signing

Widevine can check a signature over the *host* executable (Verified Media Path) and services use the result to decide
which tiers to serve. What the code does: `sweef-cdm` passes the CDM two host files through `VerifyCdmHost_0`: its own
executable with `<executable>.sig` as signature file, and the CDM library with `<library>.sig` if one exists. A missing
`.sig` is passed as "no signature file"; nothing is ever fabricated. `EngineInfo.VMPSigned` and the `-probe` output say
whether a signature file was present (not whether the CDM accepted it).

To get a signature for `sweef-cdm`, use castLabs EVS (below) and place the result next to the helper:

```
sweef-cdm
sweef-cdm.sig        # evs-vmp sign sweef-cdm   (appends .sig by default)
```

### castLabs EVS: what is publicly documented

This is read from castLabs' own page (`github.com/castlabs/electron-releases/wiki/EVS`, fetched while writing this;
check it again before acting, it changes):

- **EVS** is castLabs' VMP signing service. The EVS client is the Python 3.7+ module `castlabs-evs`
  (`python3 -m pip install --upgrade castlabs-evs`), with `castlabs_evs.account` (sign up, log in, refresh tokens) and
  `castlabs_evs.vmp` (sign, verify).
- The **free** service covers "production Widevine/VMP signing of macOS/Windows application packages derived from official
  releases of *Electron for Content Security*" (ECS v6 and later). It "is a free service, but requires signup", with
  rules of conduct (no extra accounts, no routine forced re-signing, choose `streaming` or `persistent` correctly;
  persistent license support was removed from the browser CDM as of ECS v42, so `streaming` is right for current builds).
- **Sweef is not an ECS application**, so the free path does not cover it. For custom Chromium/Electron adaptations
  castLabs offers **commercial VMP certification** as a partner in Google's **Widevine 3PL** program: an audit of the
  application and relevant code against the VMP requirements, after which commercial 3PL access to EVS is granted for
  selected accounts and builds are signed with 3PL-specific certificates. **The price is not published**; the contact
  given is 3pl@castlabs.com.
- "Since the **Linux** Widevine CDM does not support VMP, and thus does not require a VMP signature, EVS does not provide
  signing of Linux binaries." On Linux, Sweef needs no signature at all (but is still bound by service policies).
- Signing a custom executable: `python3 -m castlabs_evs.vmp sign path/to/executable [path/to/signature]`; without the
  second argument `.sig` is appended to the executable path. `--persistent` requests a persistent-license signature.
  Tokens expire monthly (`reauth`). Ordering: on **macOS sign for VMP before code signing**, on **Windows after code
  signing**, or the signature breaks.
- Whether a 3PL-signed *helper process* (as opposed to a signed Chromium/Electron main executable) is acceptable to
  Widevine for a given service tier is **not something this repository can establish**; ask castLabs during the audit.

### Draft: castLabs EVS signup checklist (for the project owner)

1. Decide the goal: Linux-only Widevine L3 needs no VMP and no castLabs account. Windows/macOS tiers that check VMP need
   the signing below. Many services play L3 at SD/720p without VMP; check against the target services first.
2. Mail 3pl@castlabs.com: Sweef is an independent, non-Chromium, pure-Go browser; Widevine CDM is hosted from the user's
   own installation through a separate helper process (`cmd/sweef-cdm`) using the Chromium CDM host ABI; ask whether the
   3PL program and VMP certification apply, what the audit covers for a non-Chromium host, the cost (one-off, annual,
   per-signature), turnaround, and whether the helper or the main binary is the signed host.
3. Prepare the audit material: this document, the `internal/media/cdm/widevinehost` source, the host-file list passed to
   `VerifyCdmHost_0`, the storage and privacy behaviour (per-origin directories, no distinctive identifier), and the fact
   that Sweef never bundles, downloads or modifies a CDM.
4. Once access is granted: create the EVS account (`python3 -m castlabs_evs.account signup`, confirm by mail), run
   `reauth` after 3PL access is enabled (as the page says), and add `evs-vmp sign` for `sweef-cdm.exe` (Windows, after
   code signing) and `sweef-cdm` (macOS, before code signing) to the release pipeline, shipping the `.sig` beside it.
5. Keep tokens out of the repo: CI secret, refresh monthly, re-sign only when the signature is invalid.
6. Re-check Widevine's and the services' terms; record the answers and costs in this file.

## PlayReady (Windows) and FairPlay (macOS): UNVERIFIED

Both are compile-checked only and off by default (`SWEEF_PLAYREADY=1`, `SWEEF_FAIRPLAY=1`), because nothing in them has
run. They exist to prove the interface fits platform key systems whose CDM does not return clear samples.

- **PlayReady** (`internal/media/cdm/playready`): `CoCreateInstance(CLSID_MFMediaEngineClassFactory)` ->
  `IMFMediaEngineClassFactory4::CreateContentDecryptionModuleFactory` -> `CreateContentDecryptionModuleAccess` ->
  `CreateContentDecryptionModule` -> `CreateSession`, with a Go COM object for
  `IMFContentDecryptionModuleSessionCallbacks`. The call sequence follows Chromium's `media/cdm/win`. Only
  `CLSID_MFMediaEngineClassFactory` and the origin-id property key were checked against sources; the interface IDs, other
  property keys and vtable slots are marked UNVERIFIED in the code and must be compared with the Windows SDK headers.
  Not implemented: the Media Engine playback path, so playback needs future work even if the license exchange works.
- **FairPlay** (`internal/media/cdm/fairplay`): `AVContentKeySession` with `AVContentKeySystemFairPlayStreaming`, a
  Go-registered `AVContentKeySessionDelegate`, `processContentKeyRequestWithIdentifier:`,
  `makeStreamingContentKeyRequestDataForApp:contentIdentifier:options:completionHandler:` (SPC out),
  `AVContentKeyResponse` / `processContentKeyResponse:` (CKC in). The application certificate comes in through
  `setServerCertificate`. Not implemented: the AVFoundation renderer handoff described above.
  Whether AVFoundation will serve FairPlay to a third-party app without extra Apple entitlements or agreements is not known here.

## Verification

Run: `CGO_ENABLED=0 go test ./internal/media/cdm/... ./cmd/sweef-cdm` (not `-race`: purego without cgo and the race
detector do not mix).

| What | How it is tested |
|---|---|
| Protocol framing | round trips, exact byte layout, truncation, oversize, corrupt length; typed errors across the wire |
| Registry, negotiation | table tests for configuration negotiation, requirement resolution, codec classification, reports |
| Clear Key | in-test encryption (block by block, different structure from the decryptor) for cenc, cens, cbc1, cbcs with subsamples, 8 and 16 byte IVs, patterns; init data (keyids, pssh v0/v1, webm), license validation, events, key loss on close |
| Remote backend | `Remote` <-> `Serve` over `net.Pipe`: event ordering (message before reply, key status before `Update` returns), individualization first, helper death, context cancel, protocol mismatch |
| Widevine discovery | temp directory layouts for linux/windows/darwin and amd64/arm64, version ordering, explicit path and env precedence, manifest parsing, capabilities from the manifest |
| Widevine host | struct sizes and offsets and vtable slots asserted at compile time and in tests; the entire stack `Remote -> Serve -> Engine -> purego host -> fake CDM` where the fake CDM is a Go object whose vtable is made of purego callbacks and which reads `InputBuffer_2` from raw memory, allocates buffers through `Host::Allocate`, uses `FileIO`, timers, promises from foreign threads, key status and expiration (a double argument), interface version fallback 11 -> 10, VMP host file array layout |
| Helper binary | built and run: fails cleanly without a library, is dynamically linked on Linux |
| Static browser binary | `TestKeySystemLayerStaysFreeOfDlopen`; the browser's own static-binary test (`cmd/sweef/static_linux_test.go` on the media branches) is unaffected because nothing in `cmd/sweef` imports the helper-only packages |
| Windows, macOS | `GOOS=windows|darwin go build ./... && go vet ./...` only |

## Privacy and policy notes

- Per-origin CDM storage is under the given storage directory; clearing site data must remove it.
- No distinctive identifier is offered (`distinctiveIdentifier: not-allowed`); configurations that require one are rejected.
- Widevine's and services' terms are between the user, Google and the service. Sweef only hosts a CDM the user already has.
