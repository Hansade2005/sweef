#!/bin/sh
# Generates the MSE fixtures of internal/engine/testdata/media/mse.
# Needs ffmpeg with libx264, libvpx-vp9, libopus, aac and libsvtav1. The output is committed;
# nothing at run time depends on ffmpeg.
#
# Content: 6 s, 25 fps. The picture is a solid colour that changes every second
# (red green blue yellow magenta cyan) with a moving white box; the sound is a tone whose
# frequency steps every second (440 660 880 1100 1320 1540 Hz). Both qualities show the same
# content so a test can tell from a pixel (or the pitch) where in the timeline it is.
set -e
out=${1:-.}
mkdir -p "$out"
cd "$out"

V() { # width height
  for c in red green blue yellow magenta cyan; do
    printf "color=c=%s:s=%sx%s:r=25:d=1,drawbox=x='t*%s/2':y=%s/3:w=%s/8:h=%s/4:c=white:t=fill[%s];" "$c" "$1" "$2" "$1" "$2" "$1" "$2" "$c"
  done
  printf "[red][green][blue][yellow][magenta][cyan]concat=n=6:v=1:a=0,format=yuv420p"
}

ffmpeg -v error -y -filter_complex "$(V 640 360)" -t 6 -c:v ffv1 -f matroska src_video.mkv
ffmpeg -v error -y -f lavfi -i "aevalsrc='0.5*sin(2*PI*t*(440+220*floor(t)))':s=48000:d=6" -c:a pcm_s16le src_audio.wav

GOP="-g 50 -keyint_min 50 -sc_threshold 0"
DASHOPTS="-seg_duration 2 -use_template 1 -use_timeline 1 -remove_at_exit 0"

# H.264 (Main, B-frames) in two qualities + AAC-LC, fMP4
rm -rf h264 && mkdir h264
ffmpeg -v error -y -i src_video.mkv -i src_audio.wav \
  -filter_complex "[0:v]split=2[a][b];[a]scale=320:180[v0];[b]scale=160:90[v1]" \
  -map "[v0]" -map "[v1]" -map 1:a \
  -c:v libx264 -profile:v main -preset veryfast $GOP -bf 2 -b:v:0 160k -b:v:1 50k \
  -c:a aac -b:a 40k -ac 1 -ar 48000 \
  -f dash $DASHOPTS -init_seg_name 'init-$RepresentationID$.m4s' -media_seg_name 'chunk-$RepresentationID$-$Number%05d$.m4s' \
  -adaptation_sets "id=0,streams=v id=1,streams=a" h264/manifest.mpd

# VP9 + Opus, WebM
rm -rf vp9 && mkdir vp9
ffmpeg -v error -y -i src_video.mkv -i src_audio.wav \
  -filter_complex "[0:v]scale=320:180[v0]" -map "[v0]" -map 1:a \
  -c:v libvpx-vp9 -deadline realtime -cpu-used 8 -b:v 150k $GOP -c:a libopus -b:a 32k -ac 1 \
  -f dash $DASHOPTS -dash_segment_type webm -init_seg_name 'init-$RepresentationID$.webm' -media_seg_name 'chunk-$RepresentationID$-$Number%05d$.webm' \
  -adaptation_sets "id=0,streams=v id=1,streams=a" vp9/manifest.mpd

# AV1 in fMP4 (video only)
rm -rf av1 && mkdir av1
ffmpeg -v error -y -i src_video.mkv \
  -filter_complex "[0:v]scale=160:90[v0]" -map "[v0]" \
  -c:v libsvtav1 -preset 10 -g 50 -b:v 80k \
  -f dash $DASHOPTS -init_seg_name 'init-$RepresentationID$.m4s' -media_seg_name 'chunk-$RepresentationID$-$Number%05d$.m4s' \
  -adaptation_sets "id=0,streams=v" av1/manifest.mpd

rm -f src_video.mkv src_audio.wav
