package win32

import (
	"strconv"
	"strings"
	"testing"
)

func TestCFHTMLRoundTripAndOffsets(t *testing.T) {
	frag := `<p>héllo <b>wörld</b></p>`
	data := htmlClipboardData(frag)
	if data[len(data)-1] != 0 {
		t.Fatal("CF_HTML data ends with a NUL")
	}
	s := string(data[:len(data)-1])
	field := func(name string) int {
		i := strings.Index(s, name+":")
		n, err := strconv.Atoi(s[i+len(name)+1 : i+len(name)+11])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return n
	}
	// the offsets are byte offsets into the whole block, which is UTF-8
	if got := s[field("StartFragment"):field("EndFragment")]; got != frag {
		t.Fatalf("fragment by offsets = %q", got)
	}
	if got := s[field("StartHTML"):field("EndHTML")]; !strings.HasPrefix(got, "<html>") || !strings.HasSuffix(got, "</html>") {
		t.Fatalf("document by offsets = %q", got)
	}
	if got := htmlFragment(data); got != frag {
		t.Fatalf("htmlFragment = %q", got)
	}
	// data from another application: Chrome's header style (fields in another order, shorter numbers)
	chrome := "Version:1.0\r\nStartHTML:0000000105\r\nEndHTML:0000000200\r\nStartFragment:0000000141\r\nEndFragment:0000000164\r\nSourceURL:https://x/\r\n<html>\r\n<body>\r\n<!--StartFragment--><b>x</b><!--EndFragment-->\r\n</body>\r\n</html>"
	if got := htmlFragment([]byte(chrome)); got == "" {
		t.Fatal("a header written by another application must be read")
	}
	if htmlFragment([]byte("no header at all")) != "" {
		t.Fatal("garbage yields no fragment")
	}
}
