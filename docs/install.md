# Installing Sweef

Sweef is **alpha** software. The installers below work, but they are not signed yet, so Windows and macOS
will warn you (details and workarounds per OS below). Linux packages work without any warning.

Pick your system:

- [Linux](#linux)
- [macOS](#macos)
- [Windows](#windows)
- [Verify your download](#verify-your-download)
- [Making Sweef your default browser](#making-sweef-your-default-browser)
- [For maintainers: cutting a release](#for-maintainers-cutting-a-release)

Downloads live on the [Releases page](https://github.com/sweefbrowser/sweef/releases). `<arch>` below is
`amd64` (most PCs) or `arm64` (Apple-silicon-class Arm machines, Raspberry Pi 4/5, Windows on Arm).

## Linux

**One line, no root** (installs into `~/.local`, adds a menu entry and icons, verifies the download):

```sh
curl -fsSL https://raw.githubusercontent.com/sweefbrowser/sweef/main/packaging/linux/install.sh | sh
```

Options go after `-s --`, for example `... | sh -s -- --prefix /opt/sweef --version 0.1.0-alpha.7`.
Run it with `--help` to see them all. Prefer to read it first? It is a short POSIX shell script.

**Or use a package:**

| Distro | Command |
|---|---|
| Debian, Ubuntu, Mint | `sudo apt install ./sweef_<version>_linux_<arch>.deb` |
| Fedora, RHEL, openSUSE | `sudo dnf install ./sweef_<version>_linux_<arch>.rpm` (or `sudo zypper install ...`) |
| Anything else | download the `.AppImage`, `chmod +x` it, run it |
| Portable | unpack the `.tar.gz`, run `./bin/sweef gui` |

Start it from your application menu ("Sweef") or run `sweef gui`.

**Uninstall**

- Packages: `sudo apt remove sweef` / `sudo dnf remove sweef`
- `install.sh`: `~/.local/bin/sweef-uninstall` (or `sh install.sh --uninstall --prefix <dir>`)
- AppImage / tarball: delete the file or folder.

Your browsing data is never removed by uninstalling.

## macOS

Requires macOS 12 or later, Intel or Apple silicon (one universal download).

1. Download `sweef_<version>_darwin_universal.dmg`, open it, drag **Sweef** to **Applications**.
2. First launch: see the box below.

> **Unsigned alpha builds and Gatekeeper.** Until we have an Apple Developer ID certificate and have
> notarized the app, macOS will refuse to open it ("Sweef is damaged" or "cannot be opened because the
> developer cannot be verified"). The app is not damaged; it is just not notarized. Either:
>
> - Run `xattr -dr com.apple.quarantine /Applications/Sweef.app` once, then open it normally, **or**
> - Try to open it, then go to *System Settings, Privacy & Security*, scroll down and click **Open Anyway**.
>
> On macOS 15 (Sequoia) and later the old "right-click, Open" shortcut no longer bypasses Gatekeeper,
> so use one of the two methods above. Only do this for files you downloaded from the official Releases page
> and checked against the checksums.

**Uninstall:** drag Sweef from Applications to the Trash.

## Windows

Requires Windows 10 or 11 (64-bit, Intel/AMD or Arm).

1. Download `sweef_<version>_windows_<arch>_setup.exe` and run it.
2. Choose **Only for me** (default, no administrator rights needed) or **All users**.
3. Tick "Choose Sweef as my default browser" on the last page if you want that
   (see [below](#making-sweef-your-default-browser)).

Silent install for IT: `setup.exe /S /currentuser` or `/S /allusers`, optionally `/D=C:\path` (must be last).
There is also a portable `.zip` (no registry changes: unzip and run `SweefLauncher.exe`), and a
[winget](https://learn.microsoft.com/windows/package-manager/) and [Scoop](https://scoop.sh) manifest attached to
each release for maintainers to submit.

> **SmartScreen.** The installer is not code-signed yet, and even once it is, a brand new publisher starts with
> no SmartScreen reputation. You will probably see "Windows protected your PC". Click **More info**, then
> **Run anyway**. A signed build removes the "unknown publisher" wording but SmartScreen can still warn until
> enough people have downloaded the file; that is how the reputation system works and we cannot skip it.
> Antivirus programs sometimes flag unsigned Go programs; checking the checksum below tells you the file is the
> one we published.

**Uninstall:** *Settings, Apps, Installed apps, Sweef, Uninstall* (or `uninstall.exe` in the install folder).
Your browsing data in `%APPDATA%\Sweef` is kept.

## Verify your download

Every release has a `SHA256SUMS` file.

```sh
# Linux / macOS
sha256sum -c SHA256SUMS --ignore-missing        # macOS: shasum -a 256 -c SHA256SUMS --ignore-missing
# Windows PowerShell
(Get-FileHash .\sweef_<version>_windows_amd64_setup.exe -Algorithm SHA256).Hash   # compare with SHA256SUMS
```

Releases are also built with GitHub build provenance attestations, which prove the file was built by this
repository's release workflow from a specific commit:

```sh
gh attestation verify sweef_<version>_linux_amd64.deb --repo sweefbrowser/sweef
```

`install.sh` checks the tarball against `SHA256SUMS` from the same release. That catches corrupted or
mismatched downloads; to guard against a compromised release page, pass a hash you got elsewhere:
`--sha256 <hex>`.

## Making Sweef your default browser

Installing does **not** silently change your default. Sweef registers itself as a *candidate* browser for
`http`, `https`, `ftp` links and `.html`/`.htm`/`.xhtml` files, and you choose it:

- **Windows:** the installer writes the standard registration (`RegisteredApplications`, `Capabilities` with
  `URLAssociations`/`FileAssociations`, `Clients\StartMenuInternet`). Windows 10/11 deliberately don't let
  installers pick the default, so open *Settings, Apps, Default apps, Sweef* and set it there (the installer's
  last page opens this for you). Per-user installs register under HKCU, all-users installs under HKLM.
- **macOS:** the app declares the `http`/`https` URL schemes and HTML document types. Pick it in
  *System Settings, Desktop & Dock, Default web browser*.
- **Linux:** the desktop entry declares `x-scheme-handler/http`, `https`, `ftp`, `text/html` and
  `application/xhtml+xml`. Choose it in your desktop's settings or run
  `xdg-settings set default-web-browser sweef.desktop`. The `.deb` also registers `x-www-browser` and
  `gnome-www-browser` alternatives at a low priority (25) so it never becomes the default by itself; switch with
  `sudo update-alternatives --config x-www-browser`.

PDF files are intentionally not claimed: Sweef will not take over your PDF viewer.

---

## For maintainers: cutting a release

Everything is built by pure-Go cross-compilation (`CGO_ENABLED=0`), so one Ubuntu runner builds all six
targets (linux/darwin/windows x amd64/arm64) and the Linux and Windows packages. A macOS runner builds the
`.dmg`.

### Cut it

```sh
git tag v0.1.0-alpha.3          # alpha.N; the pipeline always publishes a GitHub *pre-release*
git push origin v0.1.0-alpha.3
```

That triggers `.github/workflows/release.yml`: tests on three OSes, a reproducibility check (two cold builds must
be byte-identical), build, package, sign if secrets are present, `SHA256SUMS`, provenance attestation, the
pre-release with notes from `packaging/release-notes.md.tmpl`, and `latest.json` for the future updater.
You can also run the workflow manually (*Actions, release, Run workflow*); it only publishes if you tick `publish`.

Version: the tag (`vX.Y.Z[-pre]`), else `0.1.0-alpha.<commit count>`. It becomes `0.1.0~alpha.N` for Debian,
Release `0.alpha.N` for RPM, `0.1.0.N` for the Windows file version and macOS bundle version.

**Prerequisite in the code:** `cmd/sweef/main.go` must declare `var version = "dev"` (and `var commit`), not a
`const`, otherwise `-ldflags -X main.version=...` is silently ignored and `sweef version` keeps printing the old
number. The release build refuses to continue in that case (`STRICT_VERSION=1`).

### Build locally

```sh
scripts/release/build.sh                       # dist/bin/<os>_<arch>/..., dist/bin/SHA256SUMS
scripts/release/build.sh --check-repro         # also proves byte-identical rebuilds
scripts/release/package-all.sh                 # linux + windows + (unsigned) macOS zip; dmg needs macOS
scripts/release/checksums.sh && scripts/release/verify.sh   # sanity checks on the artifacts
```

Tools used on Linux: `dpkg-deb`, `rpmbuild` (package `rpm`), `makensis` (`nsis`), `osslsigncode`, `rsvg-convert`
(`librsvg2-bin`), `zip`; AppImage downloads a pinned `appimagetool` automatically. Optional validators:
`lintian`, `desktop-file-validate`, `appstreamcli`.

### Secrets

None are required for an alpha. Each missing group makes that signing step print a loud warning and be skipped.
**Never commit any of these; set them in GitHub, Settings, Secrets and variables, Actions.**

| Secret | What | Where to get it |
|---|---|---|
| `MACOS_CERT_P12` | base64 of a *Developer ID Application* certificate + key exported as `.p12` (`base64 -i cert.p12`) | Apple Developer Program (US$99/year), Certificates |
| `MACOS_CERT_PASSWORD` | the `.p12` export password | you choose it on export |
| `APPLE_ID` | Apple ID email of the developer account | |
| `APPLE_TEAM_ID` | 10-character Team ID | developer.apple.com, Membership |
| `APPLE_APP_PASSWORD` | app-specific password for notarization | appleid.apple.com, Sign-In and Security |
| `WINDOWS_CERT_PFX` | base64 of an Authenticode code-signing `.pfx` (`base64 -w0 cert.pfx`) | a certificate authority (OV or EV; EV gives the best SmartScreen standing) |
| `WINDOWS_CERT_PASSWORD` | the `.pfx` password | |
| `UPDATE_SIGNING_KEY` | base64 ed25519 seed that signs `latest.json` | `go run ./packaging/tools/pkgtool keygen`; embed the printed **public** key in the app when the updater is wired in |

Linux packages are not GPG-signed yet (a `GPG_PRIVATE_KEY` for apt/rpm repository signing is a later step).
Windows hardware-token (EV) certificates cannot be used from a hosted runner; for those use a cloud signing
service or a self-hosted runner and swap the step in `packaging/windows/sign.sh`.

Other accounts you may want: a `microsoft/winget-pkgs` fork (to submit `dist/winget/*`), a Scoop bucket, and
(optionally) a domain for a stable download and `latest.json` URL.

### Open items the owner must decide

- **License.** The repository has no `LICENSE` file. Packages currently say "proprietary/TODO"
  (`packaging/linux/sweef.metainfo.xml.in`, the Debian `copyright`, the RPM spec, winget and Scoop manifests).
  Set the real SPDX id before any public release.
- **Bundle identifier / reverse-DNS id** is `io.github.sweefbrowser.sweef` (macOS bundle, AppStream). Change it
  once if you own a domain you would rather use; it cannot change later without breaking user settings.
- **Maintainer address** for the Debian/RPM metadata is a placeholder (`MAINTAINER` env var overrides it).
- **Actions pinning:** workflows use major version tags with `TODO: pin by SHA` comments; replace with commit SHAs.
- The window class the Linux desktop entry matches is `StartupWMClass=sweef`; the X11 shell must set `WM_CLASS`
  to `sweef` (and the Windows shell should set an AppUserModelID) for taskbar grouping.

### What is *not* verified yet

macOS packaging steps that need macOS (`codesign`, `hdiutil`, `notarytool`), Windows signing against a real
certificate, the WiX alternative (`packaging/windows/wix`), and running the Windows installer. They are written
to be run by the workflow on the right runner; expect to fix small issues on the first real tag.
