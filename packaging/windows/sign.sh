#!/usr/bin/env bash
# Authenticode-sign files with osslsigncode. Skips cleanly (loud warning) when no certificate is configured.
#   packaging/windows/sign.sh file.exe [file2.exe ...]
# Secrets: WINDOWS_CERT_PFX (base64 of the .pfx), WINDOWS_CERT_PASSWORD
# Optional: WINDOWS_TIMESTAMP_URL (default http://timestamp.digicert.com)
# On a Windows runner with signtool instead: set SIGNTOOL=1 to use signtool.exe.
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
[ $# -gt 0 ] || die "usage: sign.sh file..."

if [ -z "${WINDOWS_CERT_PFX:-}" ] || [ -z "${WINDOWS_CERT_PASSWORD:-}" ]; then
  warn "WINDOWS_CERT_PFX / WINDOWS_CERT_PASSWORD not set: Windows binaries are NOT Authenticode-signed. SmartScreen will warn users ('Windows protected your PC') and may flag the download."
  exit 0
fi
TS="${WINDOWS_TIMESTAMP_URL:-http://timestamp.digicert.com}"
TS_LIST="$TS http://timestamp.sectigo.com http://time.certum.pl"
[ -n "${WINDOWS_TIMESTAMP_URL:-}" ] && TS_LIST="$WINDOWS_TIMESTAMP_URL"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
printf '%s' "$WINDOWS_CERT_PFX" | openssl base64 -d -A > "$tmp/cert.pfx"

for f in "$@"; do
  if [ "${SIGNTOOL:-0}" = 1 ]; then
    signtool sign /fd sha256 /td sha256 /tr "$TS" /f "$tmp/cert.pfx" /p "$WINDOWS_CERT_PASSWORD" /d "Sweef" /du "$HOMEPAGE" "$f"
  else
    need osslsigncode "apt install osslsigncode"
    ok=0
    # try several RFC 3161 timestamp servers; an un-timestamped signature dies with the certificate, so never fall back to none
    for ts in $TS_LIST; do
      if osslsigncode sign -pkcs12 "$tmp/cert.pfx" -pass "$WINDOWS_CERT_PASSWORD" -h sha256 \
           -n "Sweef" -i "$HOMEPAGE" -t "$ts" -in "$f" -out "$tmp/signed.bin" >/dev/null 2>&1; then ok=1; break; fi
      log "timestamp server $ts failed, trying next"
    done
    [ "$ok" = 1 ] || die "signing $f failed with every timestamp server"
    mv -f "$tmp/signed.bin" "$f"
    osslsigncode verify -in "$f" >/dev/null 2>&1 || log "note: local verify reports the chain as untrusted (normal without the CA bundle); the signature was written"
  fi
  log "signed $f"
done
