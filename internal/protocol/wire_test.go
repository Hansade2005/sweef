package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	m := &Message{JSONRPC: "2.0", ID: 7, Method: "Page.navigate", Params: json.RawMessage(`{"url":"http://x"}`)}
	if err := WriteMsg(&buf, m); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadMsg(&buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 || got.Method != "Page.navigate" || string(got.Params) != `{"url":"http://x"}` {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestBadMagic(t *testing.T) {
	buf := []byte{'X', 'X', 'X', 'X', 1, 0, 0, 0}
	r := bytes.NewReader(buf)
	if _, _, err := ReadMsg(r, nil); err != ErrBadMagic {
		t.Fatalf("want ErrBadMagic, got %v", err)
	}
}

func TestResponseError(t *testing.T) {
	var b bytes.Buffer
	err := WriteMsg(&b, &Message{JSONRPC: "2.0", ID: 1, Error: &RpcError{Code: -32601, Message: "not found"}})
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := ReadMsg(&b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Error == nil || m.Error.Code != -32601 {
		t.Fatalf("bad error frame: %+v", m)
	}
}
