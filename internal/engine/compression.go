package engine

// compression.go: CompressionStream / DecompressionStream (gzip, deflate, deflate-raw) on Go's
// compress packages. Players and analytics code (YouTube's logging, for one) use them and fall back to a
// slow script implementation when they are missing.

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
)

//go:embed compression_prelude.js
var compressionPreludeJS string

//go:embed streams_prelude.js
var streamsPreludeJS string

func init() {
	// One extension, streams first: CompressionStream is built on the TransformStream defined there.
	registerPrelude("compression", streamsPreludeJS+"\n;\n"+compressionPreludeJS)
	registerHostExtension("compression", func(j *jsVM) map[string]hostFn {
		return map[string]hostFn{
			// compressBytes(format, mode, base64) -> base64; mode "c" compresses, "d" decompresses
			"compressBytes": func(a []any) (any, error) {
				in, err := base64.StdEncoding.DecodeString(argS(a, 2))
				if err != nil {
					return nil, fmt.Errorf("TypeError: bad data")
				}
				var out []byte
				if argS(a, 1) == "c" {
					out, err = deflateBytes(argS(a, 0), in)
				} else {
					out, err = inflateBytes(argS(a, 0), in)
				}
				if err != nil {
					return nil, fmt.Errorf("TypeError: %v", err)
				}
				return base64.StdEncoding.EncodeToString(out), nil
			},
		}
	})
}

func deflateBytes(format string, in []byte) ([]byte, error) {
	var b bytes.Buffer
	var w io.WriteCloser
	switch format {
	case "gzip":
		w = gzip.NewWriter(&b)
	case "deflate":
		w = zlib.NewWriter(&b)
	case "deflate-raw":
		fw, err := flate.NewWriter(&b, flate.DefaultCompression)
		if err != nil {
			return nil, err
		}
		w = fw
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	if _, err := w.Write(in); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// maxInflate bounds the size of decompressed data (a decompression bomb must not exhaust memory).
const maxInflate = 256 << 20

func inflateBytes(format string, in []byte) ([]byte, error) {
	var r io.Reader
	var err error
	switch format {
	case "gzip":
		r, err = gzip.NewReader(bytes.NewReader(in))
	case "deflate":
		r, err = zlib.NewReader(bytes.NewReader(in))
	case "deflate-raw":
		r = flate.NewReader(bytes.NewReader(in))
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(r, maxInflate+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxInflate {
		return nil, fmt.Errorf("decompressed data too large")
	}
	return out, nil
}
