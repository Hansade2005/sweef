package av1

import (
	"fmt"
	"os"
	"testing"
)

func TestDbg(t *testing.T) {
	for _, f := range []string{"base", "ss"} {
		data, _ := os.ReadFile(os.Getenv("W") + "/" + f + ".obu")
		d := NewDecoder()
		for pos := 0; pos < len(data); {
			b := &bitReader{buf: data[pos:]}
			b.f(1)
			typ := b.f(4)
			ext := b.flag()
			b.flag()
			b.f(1)
			if ext {
				b.f(8)
			}
			size := b.leb128()
			hl := b.pos / 8
			d.Decode(data[pos : pos+hl+size])
			if typ == obuFrame && d.fh != nil {
				fmt.Println(f, "type", d.fh.frameType, "show", d.fh.showFrame, "oh", d.fh.orderHint, "refs", d.fh.refFrameIdx, "refresh", d.fh.refreshFlags, "roh", d.refOrderHint)
			}
			pos += hl + size
		}
	}
}
