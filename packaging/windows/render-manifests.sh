#!/usr/bin/env bash
# Fill the winget and Scoop templates from the release SHA256SUMS.
#   packaging/windows/render-manifests.sh   -> dist/winget/*.yaml and dist/scoop/sweef.json
# Needs dist/SHA256SUMS (scripts/release/checksums.sh). Submit dist/winget to microsoft/winget-pkgs by PR.
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
resolve_all
SUMS="$DIST/SHA256SUMS"
[ -f "$SUMS" ] || die "missing $SUMS"
REL="${RELEASE_BASE_URL:-https://github.com/sweefbrowser/sweef/releases/download/v$VERSION}"
sum_of() { awk -v n="$1" '$2==n {print toupper($1)}' "$SUMS"; }
setup_amd="sweef_${VERSION}_windows_amd64_setup.exe"; setup_arm="sweef_${VERSION}_windows_arm64_setup.exe"
zip_amd="sweef_${VERSION}_windows_amd64.zip";         zip_arm="sweef_${VERSION}_windows_arm64.zip"
for n in "$setup_amd" "$setup_arm" "$zip_amd" "$zip_arm"; do [ -n "$(sum_of "$n")" ] || die "$n not in SHA256SUMS"; done
date_iso="$(fmt_epoch "$SOURCE_DATE_EPOCH" %Y-%m-%d)"
out="$DIST/winget"; mkdir -p "$out" "$DIST/scoop"
render() { # template out urlAmd shaAmd urlArm shaArm
  sed -e "s|@VERSION@|$VERSION|g" -e "s|@DATE@|$date_iso|g" \
      -e "s|@URL_AMD64@|$3|g" -e "s|@SHA_AMD64@|$4|g" -e "s|@URL_ARM64@|$5|g" -e "s|@SHA_ARM64@|$6|g" "$1" > "$2"
}
for t in "$ROOT"/packaging/windows/winget/*.tmpl; do
  b="$(basename "$t" .tmpl)"
  render "$t" "$out/$b" "$REL/$setup_amd" "$(sum_of "$setup_amd")" "$REL/$setup_arm" "$(sum_of "$setup_arm")"
done
# Scoop uses lowercase hashes
render "$ROOT/packaging/windows/scoop/sweef.json.tmpl" "$DIST/scoop/sweef.json" \
  "$REL/$zip_amd" "$(sum_of "$zip_amd" | tr 'A-F' 'a-f')" "$REL/$zip_arm" "$(sum_of "$zip_arm" | tr 'A-F' 'a-f')"
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$DIST/scoop/sweef.json"
log "rendered $out and $DIST/scoop/sweef.json"
