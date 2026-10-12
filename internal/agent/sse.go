package agent

import (
	"bufio"
	"bytes"
	"io"
	"unicode/utf8"
)

// sse.go - a small allocation-free Server-Sent-Events reader and the hot-path
// extractor for content_block_delta events.

// sseReader yields (event, data) pairs. The returned slices alias internal
// buffers and are valid until the next call to next.
type sseReader struct {
	br   *bufio.Reader
	name []byte
	data []byte
	long []byte // accumulates lines longer than the bufio buffer
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{br: bufio.NewReaderSize(r, 64<<10)}
}

func (r *sseReader) readLine() ([]byte, error) {
	line, err := r.br.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		r.long = append(r.long[:0], line...)
		for err == bufio.ErrBufferFull {
			line, err = r.br.ReadSlice('\n')
			r.long = append(r.long, line...)
		}
		line = r.long
	}
	return line, err
}

func (r *sseReader) next() (name, data []byte, err error) {
	r.name = r.name[:0]
	r.data = r.data[:0]
	have := false
	for {
		line, rerr := r.readLine()
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0: // blank line: dispatch
				if have {
					return r.name, r.data, nil
				}
			case line[0] == ':': // comment / keep-alive
			case bytes.HasPrefix(line, []byte("event:")):
				r.name = append(r.name[:0], trimSP(line[6:])...)
				have = true
			case bytes.HasPrefix(line, []byte("data:")):
				if len(r.data) > 0 {
					r.data = append(r.data, '\n')
				}
				r.data = append(r.data, trimSP(line[5:])...)
				have = true
			}
		} else if rerr == nil {
			continue
		}
		if rerr != nil {
			if rerr == io.EOF && have && (len(r.data) > 0 || len(r.name) > 0) {
				return r.name, r.data, nil
			}
			return nil, nil, rerr
		}
	}
}

func trimSP(b []byte) []byte {
	if len(b) > 0 && b[0] == ' ' {
		return b[1:]
	}
	return b
}

// ---- content_block_delta fast path ----------------------------------------

type deltaKind uint8

const (
	deltaOther deltaKind = iota
	deltaText
	deltaInputJSON
	deltaThinking
	deltaSignature
)

// parseDelta extracts index, delta kind and the delta payload from a
// content_block_delta event without allocating. scratch is reused for
// unescaping. ok=false means "use the slow encoding/json path".
func parseDelta(data []byte, scratch *[]byte) (idx int, kind deltaKind, val []byte, ok bool) {
	i := bytes.Index(data, []byte(`"index":`))
	if i < 0 {
		return
	}
	p := i + len(`"index":`)
	for p < len(data) && data[p] == ' ' {
		p++
	}
	n, d := 0, 0
	for p < len(data) && data[p] >= '0' && data[p] <= '9' {
		n = n*10 + int(data[p]-'0')
		p++
		d++
	}
	if d == 0 {
		return
	}
	idx = n
	j := bytes.Index(data, []byte(`"delta":`))
	if j < 0 {
		return
	}
	p = j + len(`"delta":`)
	for p < len(data) && data[p] == ' ' {
		p++
	}
	if p >= len(data) || data[p] != '{' {
		return
	}
	p++
	var typ []byte
	for {
		for p < len(data) && (data[p] == ' ' || data[p] == ',') {
			p++
		}
		if p >= len(data) {
			return
		}
		if data[p] == '}' {
			break
		}
		if data[p] != '"' {
			return
		}
		ks := p + 1
		ke := indexQuote(data, ks)
		if ke < 0 {
			return
		}
		key := data[ks:ke]
		p = ke + 1
		for p < len(data) && data[p] == ' ' {
			p++
		}
		if p >= len(data) || data[p] != ':' {
			return
		}
		p++
		for p < len(data) && data[p] == ' ' {
			p++
		}
		if p >= len(data) || data[p] != '"' {
			// null / number / object values: only legal for fields we don't need
			// (e.g. "stop_reason":null). Give up on the fast path.
			return
		}
		vs := p + 1
		ve := indexQuote(data, vs)
		if ve < 0 {
			return
		}
		raw := data[vs:ve]
		p = ve + 1
		switch string(key) {
		case "type":
			typ = raw
		case "text":
			kind, val = deltaText, raw
		case "partial_json":
			kind, val = deltaInputJSON, raw
		case "thinking":
			kind, val = deltaThinking, raw
		case "signature":
			kind, val = deltaSignature, raw
		}
	}
	_ = typ
	if kind == deltaOther {
		return idx, deltaOther, nil, true
	}
	if bytes.IndexByte(val, '\\') >= 0 {
		var uok bool
		*scratch, uok = unescapeInto((*scratch)[:0], val)
		if !uok {
			return 0, deltaOther, nil, false
		}
		val = *scratch
	}
	return idx, kind, val, true
}

// indexQuote returns the index of the first unescaped '"' at or after from.
func indexQuote(b []byte, from int) int {
	for i := from; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// unescapeInto decodes JSON string escapes from src and appends to dst.
func unescapeInto(dst, src []byte) ([]byte, bool) {
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != '\\' {
			dst = append(dst, c)
			continue
		}
		i++
		if i >= len(src) {
			return dst, false
		}
		switch src[i] {
		case '"', '\\', '/':
			dst = append(dst, src[i])
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r, ok := hex4(src, i+1)
			if !ok {
				return dst, false
			}
			i += 4
			if r >= 0xD800 && r < 0xDC00 { // surrogate pair
				if i+6 < len(src)+0 && src[i+1] == '\\' && src[i+2] == 'u' {
					if r2, ok2 := hex4(src, i+3); ok2 && r2 >= 0xDC00 && r2 < 0xE000 {
						r = 0x10000 + (r-0xD800)<<10 + (r2 - 0xDC00)
						i += 6
					}
				}
			}
			dst = utf8.AppendRune(dst, r)
		default:
			return dst, false
		}
	}
	return dst, true
}

func hex4(b []byte, at int) (rune, bool) {
	if at+4 > len(b) {
		return 0, false
	}
	var r rune
	for _, c := range b[at : at+4] {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			r |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return r, true
}
