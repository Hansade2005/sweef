// Package protocol implements Sweef's wire format: a length-prefixed,
// JSON-RPC-2.0 compatible framing that is deliberately close enough to the
// Chrome DevTools Protocol (CDP) that existing CDP clients can attach with
// minimal shimming, while staying tiny and streaming-friendly.
//
// Frame layout (little-endian):
//
//	[4 bytes] magic 'S','W','E','F'
//	[4 bytes] payload length (uint32, max 64 MiB)
//	[N bytes] UTF-8 JSON payload (a JsonRpc Request/Response/Notification)
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	Magic       = "SWF1"
	MaxFrameLen = 64 << 20 // 64 MiB hard cap keeps hostile peers from OOM-ing us
)

var (
	ErrBadMagic = errors.New("protocol: bad magic")
	ErrTooLarge = errors.New("protocol: frame exceeds limit")
	ErrClosed   = errors.New("protocol: connection closed")
)

// Message is one JsonRpc envelope. Either Method != "" (request/notification),
// or Result/Error != nil (response).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RpcError       `json:"error,omitempty"`
}

type RpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// WriteMsg serialises m into one frame and writes it.
func WriteMsg(w io.Writer, m *Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameLen {
		return ErrTooLarge
	}
	hdr := make([]byte, 8)
	copy(hdr[:4], Magic)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(b)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadMsg reads exactly one frame. Caller-owned buffer is reused safely.
func ReadMsg(r io.Reader, buf []byte) (*Message, []byte, error) {
	if len(buf) < 8 {
		buf = make([]byte, 4096)
	}
	if _, err := io.ReadFull(r, buf[:8]); err != nil {
		return nil, buf, err
	}
	if string(buf[:4]) != Magic {
		return nil, buf, ErrBadMagic
	}
	n := binary.LittleEndian.Uint32(buf[4:])
	if n > MaxFrameLen {
		return nil, buf, ErrTooLarge
	}
	if int(n) > cap(buf) {
		buf = make([]byte, n)
	} else {
		buf = buf[:n]
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, buf, err
	}
	m := &Message{}
	if err := json.Unmarshal(buf, m); err != nil {
		return nil, buf, err
	}
	return m, buf, nil
}

// Conn wraps a bidirectional stream with mutexed writes so many goroutines
// (engine events, RPC replies, MCP notifications) can share one socket.
type Conn struct {
	rw     io.ReadWriteCloser
	wmu    sync.Mutex
	closed chan struct{}
	once   sync.Once
}

func NewConn(rw io.ReadWriteCloser) *Conn {
	c := &Conn{rw: rw, closed: make(chan struct{})}
	return c
}

func (c *Conn) Write(m *Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	return WriteMsg(c.rw, m)
}

func (c *Conn) Read(buf []byte) (*Message, []byte, error) { return ReadMsg(c.rw, buf) }

func (c *Conn) Close() error {
	err := c.rw.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func (c *Conn) Done() <-chan struct{} { return c.closed }
