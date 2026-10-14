// Copyright 2017 Hajime Hoshi
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mp3

import (
	"bytes"
	"errors"
	"io"

	"github.com/hajimehoshi/go-mp3/internal/consts"
	"github.com/hajimehoshi/go-mp3/internal/frame"
)

// Sweef addition: a decoder that is fed one MP3 frame at a time, so a player
// can start anywhere in a file it has indexed without reading (or scanning)
// all of it first. The bit reservoir and the synthesis filter state carry
// over from frame to frame, so a seek should feed a few frames of lead-in.

// FrameDecoder decodes consecutive MPEG-1/2 Layer III frames.
type FrameDecoder struct {
	prev *frame.Frame
}

// NewFrameDecoder returns a decoder with empty state.
func NewFrameDecoder() *FrameDecoder { return &FrameDecoder{} }

// Reset forgets the state of the previous frames (after a seek).
func (d *FrameDecoder) Reset() { d.prev = nil }

// ErrNoData is returned for a frame that depends on bit reservoir data the
// decoder does not have (the first frames after Reset): it yields no audio.
var ErrNoData = errors.New("mp3: frame needs data of earlier frames")

// Decode decodes one complete frame (header included) and returns 16-bit
// little-endian stereo PCM (always two channels, like Decoder), together with
// the sample rate of the frame.
func (d *FrameDecoder) Decode(data []byte) (pcm []byte, sampleRate int, err error) {
	src := &source{reader: bytes.NewReader(data)}
	f, _, err := frame.Read(src, 0, d.prev)
	if err != nil {
		if err == io.EOF {
			return nil, 0, io.ErrUnexpectedEOF
		}
		if _, ok := err.(*consts.UnexpectedEOF); ok {
			return nil, 0, ErrNoData
		}
		return nil, 0, err
	}
	d.prev = f
	sr, err := f.SamplingFrequency()
	if err != nil {
		return nil, 0, err
	}
	return f.Decode(), sr, nil
}
