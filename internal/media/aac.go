package media

// nativeAAC reports whether the pure-Go AAC decoder handles the track.
func nativeAAC(a *Track) bool { return false }

func decodeAACStream(data []byte, as *audioStream) (*PCM, error) { return nil, ErrUnsupported }
