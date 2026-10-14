// Package mediarec is the engine behind MediaRecorder: it takes live audio chunks and video frames, encodes them
// (Opus, VP8), packs them into a streamable container (WebM or Ogg) and hands out byte chunks that, concatenated,
// form a valid file. It knows nothing about JavaScript; internal/engine/mediarecorder.go connects it to pages and
// docs/mediarecorder.md describes the behaviour a page sees.
package mediarec
