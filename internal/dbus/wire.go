// Package dbus is a small pure-Go client of the D-Bus message bus: the wire format (marshalling per the D-Bus
// specification), the EXTERNAL authentication, method calls, signal delivery and serving method calls. It is
// what the desktop notification backend (org.freedesktop.Notifications) is built on and is deliberately limited
// to the unix transports and to what that needs: no file descriptor passing, no introspection helpers.
package dbus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// Variant is a value with a dynamic type (signature "v").
type Variant struct {
	Sig   string
	Value any
}

// ObjectPath is a value of type "o".
type ObjectPath string

// Signature is a value of type "g".
type Signature string

// Struct is the decoded form of a "(...)" value.
type Struct []any

const maxArray = 64 << 20

type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

type encoder struct {
	buf   []byte
	order byteOrder
}

func (e *encoder) align(n int) {
	for len(e.buf)%n != 0 {
		e.buf = append(e.buf, 0)
	}
}

func (e *encoder) u32(v uint32) {
	e.align(4)
	e.buf = e.order.AppendUint32(e.buf, v)
}

// splitType returns the first complete type of sig and the rest.
func splitType(sig string) (string, string, error) {
	if sig == "" {
		return "", "", errors.New("dbus: empty signature")
	}
	switch sig[0] {
	case 'a':
		t, rest, err := splitType(sig[1:])
		if err != nil {
			return "", "", err
		}
		return "a" + t, rest, nil
	case '(', '{':
		closer := byte(')')
		if sig[0] == '{' {
			closer = '}'
		}
		depth := 0
		for i := 0; i < len(sig); i++ {
			switch sig[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					if sig[i] != closer {
						return "", "", fmt.Errorf("dbus: unbalanced signature %q", sig)
					}
					return sig[:i+1], sig[i+1:], nil
				}
			}
		}
		return "", "", fmt.Errorf("dbus: unterminated signature %q", sig)
	case 'y', 'b', 'n', 'q', 'i', 'u', 'x', 't', 'd', 's', 'o', 'g', 'v', 'h':
		return sig[:1], sig[1:], nil
	}
	return "", "", fmt.Errorf("dbus: unknown type code %q", sig[0])
}

func alignOf(t byte) int {
	switch t {
	case 'y', 'g', 'v':
		return 1
	case 'n', 'q':
		return 2
	case 'b', 'i', 'u', 's', 'o', 'a', 'h':
		return 4
	}
	return 8 // x t d ( {
}

func (e *encoder) encode(sig string, v any) error {
	switch sig[0] {
	case 'y':
		n, err := toUint(v)
		if err != nil {
			return err
		}
		e.buf = append(e.buf, byte(n))
	case 'b':
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("dbus: %T is not a bool", v)
		}
		if b {
			e.u32(1)
		} else {
			e.u32(0)
		}
	case 'n', 'q':
		n, err := toUint(v)
		if err != nil {
			return err
		}
		e.align(2)
		e.buf = e.order.AppendUint16(e.buf, uint16(n))
	case 'i', 'u':
		n, err := toUint(v)
		if err != nil {
			return err
		}
		e.u32(uint32(n))
	case 'x', 't':
		n, err := toUint(v)
		if err != nil {
			return err
		}
		e.align(8)
		e.buf = e.order.AppendUint64(e.buf, n)
	case 'd':
		f, ok := v.(float64)
		if !ok {
			return fmt.Errorf("dbus: %T is not a float64", v)
		}
		e.align(8)
		e.buf = e.order.AppendUint64(e.buf, math.Float64bits(f))
	case 's', 'o':
		s, ok := asString(v)
		if !ok {
			return fmt.Errorf("dbus: %T is not a string", v)
		}
		if strings.IndexByte(s, 0) >= 0 {
			return errors.New("dbus: a string must not contain NUL")
		}
		e.u32(uint32(len(s)))
		e.buf = append(append(e.buf, s...), 0)
	case 'g':
		s, ok := asString(v)
		if !ok || len(s) > 255 {
			return fmt.Errorf("dbus: %T is not a signature", v)
		}
		e.buf = append(append(e.buf, byte(len(s))), s...)
		e.buf = append(e.buf, 0)
	case 'v':
		vv, ok := v.(Variant)
		if !ok {
			return fmt.Errorf("dbus: %T is not a Variant", v)
		}
		t, rest, err := splitType(vv.Sig)
		if err != nil || rest != "" {
			return fmt.Errorf("dbus: variant signature %q is not a single complete type", vv.Sig)
		}
		if err := e.encode("g", vv.Sig); err != nil {
			return err
		}
		return e.encode(t, vv.Value)
	case 'a':
		elem := sig[1:]
		rv := reflect.ValueOf(v)
		if !rv.IsValid() {
			return errors.New("dbus: nil array")
		}
		e.u32(0)
		lenAt := len(e.buf) - 4
		e.align(alignOf(elem[0]))
		start := len(e.buf)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				if err := e.encode(elem, rv.Index(i).Interface()); err != nil {
					return err
				}
			}
		case reflect.Map:
			if elem[0] != '{' {
				return errors.New("dbus: a map needs an a{..} signature")
			}
			keys := rv.MapKeys()
			sortKeys(keys)
			for _, k := range keys {
				e.align(8)
				kt, rest, err := splitType(elem[1:])
				if err != nil {
					return err
				}
				if err := e.encode(kt, k.Interface()); err != nil {
					return err
				}
				vt, _, err := splitType(rest)
				if err != nil {
					return err
				}
				if err := e.encode(vt, rv.MapIndex(k).Interface()); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("dbus: %T is not an array", v)
		}
		n := len(e.buf) - start
		if n > maxArray {
			return errors.New("dbus: array too large")
		}
		e.order.PutUint32(e.buf[lenAt:], uint32(n))
	case '(':
		e.align(8)
		fields, ok := v.([]any)
		if !ok {
			if st, ok2 := v.(Struct); ok2 {
				fields, ok = []any(st), true
			}
		}
		if !ok {
			return fmt.Errorf("dbus: %T is not a struct", v)
		}
		rest := sig[1 : len(sig)-1]
		for _, f := range fields {
			t, r, err := splitType(rest)
			if err != nil {
				return err
			}
			if err := e.encode(t, f); err != nil {
				return err
			}
			rest = r
		}
	default:
		return fmt.Errorf("dbus: cannot encode type %q", sig)
	}
	return nil
}

func sortKeys(keys []reflect.Value) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j].String() < keys[j-1].String(); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

func asString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case ObjectPath:
		return string(s), true
	case Signature:
		return string(s), true
	}
	return "", false
}

func toUint(v any) (uint64, error) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint:
		return rv.Uint(), nil
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		return uint64(rv.Int()), nil
	}
	return 0, fmt.Errorf("dbus: %T is not an integer", v)
}

// Encode marshals values according to signature sig, as a message body (or header field array) starting at an
// 8-aligned offset.
func Encode(order byteOrder, sig string, vals ...any) ([]byte, error) {
	e := &encoder{order: order}
	rest := sig
	for i := 0; rest != ""; i++ {
		t, r, err := splitType(rest)
		if err != nil {
			return nil, err
		}
		if i >= len(vals) {
			return nil, fmt.Errorf("dbus: signature %q needs more values than the %d given", sig, len(vals))
		}
		if err := e.encode(t, vals[i]); err != nil {
			return nil, err
		}
		rest = r
	}
	return e.buf, nil
}

type decoder struct {
	buf   []byte
	pos   int
	order binary.ByteOrder
	depth int
}

var errShort = errors.New("dbus: message truncated")

func (d *decoder) align(n int) error {
	for d.pos%n != 0 {
		d.pos++
	}
	if d.pos > len(d.buf) {
		return errShort
	}
	return nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.pos+n > len(d.buf) {
		return nil, errShort
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *decoder) u32() (uint32, error) {
	if err := d.align(4); err != nil {
		return 0, err
	}
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(b), nil
}

func (d *decoder) decode(sig string) (any, error) {
	d.depth++
	defer func() { d.depth-- }()
	if d.depth > 64 {
		return nil, errors.New("dbus: nesting too deep")
	}
	switch sig[0] {
	case 'y':
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		return b[0], nil
	case 'b':
		n, err := d.u32()
		return n != 0, err
	case 'n', 'q':
		if err := d.align(2); err != nil {
			return nil, err
		}
		b, err := d.take(2)
		if err != nil {
			return nil, err
		}
		n := d.order.Uint16(b)
		if sig[0] == 'n' {
			return int16(n), nil
		}
		return n, nil
	case 'i':
		n, err := d.u32()
		return int32(n), err
	case 'u', 'h':
		return d.u32()
	case 'x', 't':
		if err := d.align(8); err != nil {
			return nil, err
		}
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		n := d.order.Uint64(b)
		if sig[0] == 'x' {
			return int64(n), nil
		}
		return n, nil
	case 'd':
		if err := d.align(8); err != nil {
			return nil, err
		}
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(d.order.Uint64(b)), nil
	case 's', 'o':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		b, err := d.take(int(n))
		if err != nil {
			return nil, err
		}
		z, err := d.take(1)
		if err != nil || z[0] != 0 {
			return nil, errors.New("dbus: string is not NUL terminated")
		}
		if sig[0] == 'o' {
			return ObjectPath(b), nil
		}
		return string(b), nil
	case 'g':
		l, err := d.take(1)
		if err != nil {
			return nil, err
		}
		b, err := d.take(int(l[0]))
		if err != nil {
			return nil, err
		}
		if z, err := d.take(1); err != nil || z[0] != 0 {
			return nil, errors.New("dbus: signature is not NUL terminated")
		}
		return Signature(b), nil
	case 'v':
		s, err := d.decode("g")
		if err != nil {
			return nil, err
		}
		t, rest, err := splitType(string(s.(Signature)))
		if err != nil || rest != "" {
			return nil, errors.New("dbus: bad variant signature")
		}
		v, err := d.decode(t)
		if err != nil {
			return nil, err
		}
		return Variant{Sig: t, Value: v}, nil
	case 'a':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		if n > maxArray {
			return nil, errors.New("dbus: array too large")
		}
		elem := sig[1:]
		if err := d.align(alignOf(elem[0])); err != nil {
			return nil, err
		}
		end := d.pos + int(n)
		if end > len(d.buf) {
			return nil, errShort
		}
		if elem == "y" {
			b, _ := d.take(int(n))
			return append([]byte(nil), b...), nil
		}
		if elem[0] == '{' {
			kt, rest, _ := splitType(elem[1:])
			vt, _, _ := splitType(rest)
			m := map[any]any{}
			for d.pos < end {
				if err := d.align(8); err != nil {
					return nil, err
				}
				k, err := d.decode(kt)
				if err != nil {
					return nil, err
				}
				v, err := d.decode(vt)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if d.pos != end {
				return nil, errors.New("dbus: array length mismatch")
			}
			return m, nil
		}
		out := []any{}
		for d.pos < end {
			v, err := d.decode(elem)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if d.pos != end {
			return nil, errors.New("dbus: array length mismatch")
		}
		return out, nil
	case '(':
		if err := d.align(8); err != nil {
			return nil, err
		}
		var out Struct
		rest := sig[1 : len(sig)-1]
		for rest != "" {
			t, r, err := splitType(rest)
			if err != nil {
				return nil, err
			}
			v, err := d.decode(t)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			rest = r
		}
		return out, nil
	}
	return nil, fmt.Errorf("dbus: cannot decode type %q", sig)
}

// Decode unmarshals a body of signature sig.
func Decode(order binary.ByteOrder, sig string, data []byte) ([]any, error) {
	d := &decoder{buf: data, order: order}
	var out []any
	for rest := sig; rest != ""; {
		t, r, err := splitType(rest)
		if err != nil {
			return nil, err
		}
		v, err := d.decode(t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		rest = r
	}
	return out, nil
}
