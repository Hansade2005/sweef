#!/usr/bin/env bash
# Render the Sweef mark to PNGs and bundle .ico / .icns (pure-Go containers).
# Output: $DIST/icons/{png/<n>.png, sweef.ico, sweef.icns, sweef.svg}
set -euo pipefail
. "$(dirname "$0")/lib.sh"
resolve_epoch

SVG="$ROOT/packaging/assets/sweef.svg"
OUT="$DIST/icons"
mkdir -p "$OUT/png"
cp "$SVG" "$OUT/sweef.svg"

render() { # size out
  if command -v rsvg-convert >/dev/null 2>&1; then
    rsvg-convert -w "$1" -h "$1" -o "$2" "$SVG"
  elif command -v convert >/dev/null 2>&1; then
    convert -background none -density 384 "$SVG" -resize "${1}x${1}" "PNG32:$2"
  else
    die "need rsvg-convert (librsvg2-bin) or ImageMagick convert with an SVG delegate"
  fi
}

for n in 16 22 24 32 48 64 128 256 512 1024; do
  render "$n" "$OUT/png/$n.png"
done

PT="$(pkgtool)"
"$PT" ico  -o "$OUT/sweef.ico"  "$OUT"/png/{16,24,32,48,64,128,256}.png
"$PT" icns -o "$OUT/sweef.icns" "$OUT"/png/{16,32,64,128,256,512,1024}.png
log "icons written to $OUT"
