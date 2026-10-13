package engine

// platform_codec.go: host side of CompressionStream / DecompressionStream.
//
// Compression is push-driven (write a chunk, collect what the encoder emits).
// Decompression is pull-driven in Go, so each stream runs its decoder in a
// goroutine fed by a blocking reader; the JS side hands in a chunk and waits
// until the decoder has consumed it (it is "starved" and asks for more) or has
// produced a full output window. Output is capped per call (codecWindow) and
// the caller drains the rest with zPull as its readable side is read, so a
// small compressed chunk that expands enormously cannot flood the heap.

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/dsnet/compress/brotli"
)

const codecWindow = 64 << 10

type codec struct {
	mu   sync.Mutex
	cond *sync.Cond

	// compression
	enc io.WriteCloser
	out bytes.Buffer

	// decompression
	dec      bool
	in       []byte
	eof      bool // no more input will arrive
	starved  bool // decoder is blocked waiting for input
	done     bool // decoder goroutine finished
	err      error
	trailing bool // decoder finished cleanly but more input arrived
	abort    bool
}

func (c *codec) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.in) == 0 {
		if c.eof || c.abort {
			return 0, io.EOF
		}
		c.starved = true
		c.cond.Broadcast()
		c.cond.Wait()
	}
	c.starved = false
	n := copy(p, c.in)
	c.in = c.in[n:]
	return n, nil
}

func (c *codec) ReadByte() (byte, error) {
	var b [1]byte
	if _, err := c.Read(b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

// Write receives decoder output; it blocks while the window is full.
func (c *codec) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.out.Len() >= codecWindow && !c.abort {
		c.cond.Broadcast()
		c.cond.Wait()
	}
	if c.abort {
		return 0, io.ErrClosedPipe
	}
	c.out.Write(p)
	c.cond.Broadcast()
	return len(p), nil
}

func (c *codec) run(format string) {
	var r io.Reader
	var err error
	switch format {
	case "gzip":
		r, err = gzip.NewReader(c)
	case "deflate":
		r, err = zlib.NewReader(c)
	case "deflate-raw":
		r = flate.NewReader(c)
	case "brotli":
		r, err = brotli.NewReader(c, nil)
	}
	if err == nil {
		_, err = io.Copy(c, r)
	}
	c.mu.Lock()
	if err != nil && !errors.Is(err, io.ErrClosedPipe) {
		c.err = err
	}
	c.done = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

// take hands back up to the window of output plus whether the caller must
// pull again (decoder paused on a full window).
func (c *codec) take() map[string]any {
	n := c.out.Len()
	if n > codecWindow {
		n = codecWindow
	}
	data := make([]byte, n)
	copy(data, c.out.Next(n))
	res := map[string]any{"d": base64.StdEncoding.EncodeToString(data)}
	if c.err != nil {
		res["err"] = c.errText()
	} else if c.out.Len() > 0 || (c.dec && !c.starved && !c.done) {
		res["more"] = true
	}
	c.cond.Broadcast()
	return res
}

func (c *codec) errText() string {
	if c.trailing {
		return "unexpected data after the end of the compressed stream"
	}
	if errors.Is(c.err, io.ErrUnexpectedEOF) || errors.Is(c.err, io.EOF) {
		return "unexpected end of the compressed stream"
	}
	return "invalid compressed data: " + c.err.Error()
}

// settle waits until the decoder is starved, finished, or has a full window.
func (c *codec) settle() {
	for !(c.starved || c.done || c.out.Len() >= codecWindow) {
		c.cond.Wait()
	}
}

func (j *jsVM) codecHost(f map[string]hostFn) {
	ops := map[string]hostFn{}
	var (
		mu     sync.Mutex
		next   = 1
		stream = map[int]*codec{}
	)
	get := func(a []any) (*codec, error) {
		mu.Lock()
		defer mu.Unlock()
		c := stream[int(argI(a, 0))]
		if c == nil {
			return nil, fmt.Errorf("InvalidStateError: compression stream closed")
		}
		return c, nil
	}
	closeCodec := func(c *codec) {
		c.mu.Lock()
		c.abort = true
		c.cond.Broadcast()
		c.mu.Unlock()
	}
	j.closers = append(j.closers, func() {
		mu.Lock()
		defer mu.Unlock()
		for id, c := range stream {
			closeCodec(c)
			delete(stream, id)
		}
	})
	ops["new"] = func(a []any) (any, error) {
		format, decompress := argS(a, 0), argB(a, 1)
		c := &codec{dec: decompress}
		c.cond = sync.NewCond(&c.mu)
		switch format {
		case "gzip", "deflate", "deflate-raw":
		case "brotli":
			if !decompress {
				return nil, fmt.Errorf("TypeError: brotli compression is not supported")
			}
		default:
			return nil, fmt.Errorf("TypeError: unsupported compression format %q", format)
		}
		if decompress {
			go c.run(format)
		} else {
			switch format {
			case "gzip":
				c.enc = gzip.NewWriter(&c.out)
			case "deflate":
				c.enc = zlib.NewWriter(&c.out)
			case "deflate-raw":
				w, _ := flate.NewWriter(&c.out, flate.DefaultCompression)
				c.enc = w
			}
		}
		mu.Lock()
		id := next
		next++
		stream[id] = c
		mu.Unlock()
		return id, nil
	}
	ops["push"] = func(a []any) (any, error) {
		c, err := get(a)
		if err != nil {
			return nil, err
		}
		data, derr := base64.StdEncoding.DecodeString(argS(a, 1))
		if derr != nil {
			return nil, fmt.Errorf("TypeError: bad chunk")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.dec {
			if _, err := c.enc.Write(data); err != nil {
				return map[string]any{"err": err.Error()}, nil
			}
			return c.take(), nil
		}
		if c.done && c.err == nil && len(data) > 0 {
			c.trailing, c.err = true, io.ErrUnexpectedEOF
		}
		if c.err == nil && len(data) > 0 {
			c.in = append(c.in, data...)
			c.starved = false
			c.cond.Broadcast()
			c.settle()
			if c.done && c.err == nil && len(c.in) > 0 {
				c.trailing, c.err = true, io.ErrUnexpectedEOF
			}
		}
		return c.take(), nil
	}
	ops["pull"] = func(a []any) (any, error) {
		c, err := get(a)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.dec && c.err == nil {
			c.cond.Broadcast()
			c.settle()
		}
		return c.take(), nil
	}
	ops["end"] = func(a []any) (any, error) {
		c, err := get(a)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.dec {
			if cerr := c.enc.Close(); cerr != nil {
				return map[string]any{"err": cerr.Error()}, nil
			}
			c.eof = true
			return c.take(), nil
		}
		if !c.eof {
			c.eof = true
			c.cond.Broadcast()
		}
		for !c.done && c.out.Len() < codecWindow {
			c.cond.Wait()
		}
		if c.done && c.err == nil && len(c.in) > 0 {
			c.trailing, c.err = true, io.ErrUnexpectedEOF
		}
		res := c.take()
		if c.done && c.out.Len() == 0 {
			delete(res, "more")
		}
		return res, nil
	}
	ops["close"] = func(a []any) (any, error) {
		mu.Lock()
		id := int(argI(a, 0))
		c := stream[id]
		delete(stream, id)
		mu.Unlock()
		if c != nil {
			closeCodec(c)
		}
		return nil, nil
	}
	// One host function for the whole codec: every registration costs start-up time in every VM.
	f["codec"] = func(a []any) (any, error) {
		if len(a) == 0 {
			return nil, fmt.Errorf("TypeError: missing codec operation")
		}
		op := ops[argS(a, 0)]
		if op == nil {
			return nil, fmt.Errorf("TypeError: unknown codec operation")
		}
		return op(a[1:])
	}
}
