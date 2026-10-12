#!/bin/bash
# How the extended AV1 fixtures in internal/media/testdata were produced.
# Needs libaom (aomenc, svc_encoder_rtc, lightfield_*, built from source with
# -DENABLE_EXAMPLES=1) and libdav1d's CLI. The md5 files are per-picture MD5s
# of dav1d's raw output (--muxer yuv), or of libaom's for large scale tile.
set -e
# non-uniform tile spacing / switch frames (aomenc)
aomenc src_640.y4m --obu --limit=8 --cpu-used=4 --tile-width=1,3,2,4 --tile-height=2,3,2 -o av1_nonuni.obu
aomenc src_640.y4m --obu --limit=8 --cpu-used=4 --sframe-dist=3 --sframe-mode=1 --error-resilient=1 -o av1_sframe.obu
# spatial/temporal layers (libaom svc_encoder_rtc, output OBU)
svc_encoder_rtc -w 640 -h 360 -b 1000 -sl 2 -tl 1 -lm 5 -bl 300,700 -t 1/15 --output-obu=1 -o av1_svc_l2t1.obu src_640_12.y4m
svc_encoder_rtc -w 640 -h 360 -b 1000 -sl 3 -tl 3 -lm 8 -bl 100,150,200,100,200,300,200,350,500 -t 1/15 --output-obu=1 -o av1_svc_l3t3.obu src_640_12.y4m
dav1d -i av1_svc_l3t3.obu --alllayers 0 --muxer yuv -o l3t3_a0.yuv     # av1_svc_l3t3.md5
dav1d -i av1_svc_l3t3.obu --alllayers 1 --muxer yuv -o l3t3_a1.yuv     # .all.md5
dav1d -i av1_svc_l3t3.obu --oppoint 4 --muxer yuv -o l3t3_op4.yuv      # .op4.md5
# Annex B
aomenc src_640.y4m --obu --annexb=1 --limit=8 --lag-in-frames=8 -o avb_annexb.obu
# 12-bit 4:4:4 (ffmpeg's libaom)
ffmpeg -f lavfi -i mandelbrot=rate=15:size=352x288 -frames:v 8 -pix_fmt yuv444p12le -c:v libaom-av1 -cpu-used 6 -crf 20 -b:v 0 -f obu av1_12bit444.obu
# intra_only frames and frame_refs_short_signaling: no encoder emits the
# former and libaom the latter only rarely, so the headers of a libaom stream
# are rewritten by TestGenIntraOnly / TestGenShortSignaling (av1/gen_intraonly_test.go):
aomenc src_352.y4m --obu --limit=10 --cpu-used=4 --kf-max-dist=3 --kf-min-dist=3 -o kf.obu
AV1_GEN_ERRRES=1 AV1_GEN_IN=kf.obu AV1_GEN_OUT=av1_intraonly.obu go test -run TestGenIntraOnly ./internal/media/av1
aomenc src_640.y4m --obu --limit=12 --cpu-used=4 --lag-in-frames=0 -o g1.obu
AV1_GEN_ONLYSAME=1 AV1_GEN_IN=g1.obu AV1_GEN_OUT=av1_shortsig.obu go test -run TestGenShortSignaling ./internal/media/av1
# large scale tile: libaom lightfield pipeline (512x512 views, 4x4 light field,
# 4 anchor frames), a tile list file and libaom's own decoding of it
lightfield_encoder 512 512 lf.yuv lf.ivf 4 4 2
lightfield_bitstream_parsing lf.ivf av1ls_tiles.ivf 4 tiles.txt
lightfield_tile_list_decoder av1ls_tiles.ivf out.yuv 4 3 1             # raster output -> av1ls_tiles.md5
