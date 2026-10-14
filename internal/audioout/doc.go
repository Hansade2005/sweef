// Package audioout is the audio output layer of the browser: a mixer with a
// clocked output device behind it.
//
// Everything that makes sound (HTMLMediaElement, Web Audio, WebRTC remote
// audio) opens a Stream, writes interleaved float32 PCM into it and reads the
// stream's Position as the master clock. The mixer converts every stream to
// the device format (48 kHz, stereo), applies per-stream and per-group volume
// and mute, and hands the sum to a Backend.
//
// Backends: the real device (github.com/ebitengine/oto, pure Go on Linux,
// macOS and Windows; PulseAudio native protocol first, ALSA through purego as
// the fallback), a null sink that consumes audio in real time and discards it
// (used when no device exists: CI, containers, headless), and a test sink
// that records what was mixed. The null sink keeps the audio clock running at
// the right speed, so playback timing and A/V sync behave the same without a
// sound card.
//
// See docs/audio-output.md for the contract.
package audioout
