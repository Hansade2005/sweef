package media

// What plays, what decodes (measured against ffprobe/ffmpeg and Chromium 141
// in the tests of this package and internal/engine/media_test.go).
//
// Containers parsed in pure Go (duration, tracks, codec strings, sizes, frame
// rate, seek index): MP4/MOV/M4A (progressive and fragmented), WebM/Matroska,
// Ogg (Vorbis, Opus, Theora, FLAC), WAV, MP3 (ID3, Xing/VBRI/LAME), FLAC,
// AAC/ADTS, AVI (MJPEG), GIF. HLS/DASH manifests are not played
// (canPlayType answers "" like desktop Chromium).
//
// Audio decoded in pure Go: PCM WAV (8/16/24/32-bit int, float32/64, A-law,
// mu-law), MP3 (go-mp3, trimmed like ffmpeg), Ogg Vorbis, FLAC. Everything else
// (AAC, ...) is decoded only when ffmpeg is installed; Opus decodes natively;
// without it decodeAudioData rejects with EncodingError.
//
// Video frames (shown through the engine bitmap registry):
//
//	with ffmpeg   any video codec ffmpeg knows: one frame per currentTime
//	              (cached, asynchronous, at most two ffmpeg processes)
//	pure Go       VP8 key frames (golang.org/x/image/vp8), MJPEG in AVI/MP4,
//	              animated GIF and APNG by wall time. Inter frames of VP8/VP9/
//	              H.264/AV1 are NOT decoded: without ffmpeg a WebM VP8 video
//	              shows its key frame, and VP9/AV1/H.264/HEVC are refused
//	              (MEDIA_ERR_SRC_NOT_SUPPORTED, canPlayType "").
//
// canPlayType claims H.264/AAC only when ffmpeg exists. The element clock does
// not need decoded audio: currentTime, ended, timeupdate and seeking work for
// every container above; audio is decoded only when connected to Web Audio
// (MediaElementAudioSourceNode) or captured.
