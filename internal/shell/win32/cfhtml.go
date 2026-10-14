package win32

// cfhtml.go: the CF_HTML clipboard format, which is plain data and so is built and read on every platform (the
// Windows clipboard calls are in clipboard.go).

import (
	"fmt"
	"strings"
)

// htmlClipboardData wraps an HTML fragment in the header CF_HTML requires: byte offsets (decimal, ten digits) of
// the document and of the fragment inside it, all in UTF-8.
func htmlClipboardData(fragment string) []byte {
	const hdr = "Version:0.9\r\nStartHTML:%010d\r\nEndHTML:%010d\r\nStartFragment:%010d\r\nEndFragment:%010d\r\n"
	pre := "<html><body>\r\n<!--StartFragment-->"
	post := "<!--EndFragment-->\r\n</body></html>"
	hl := len(fmt.Sprintf(hdr, 0, 0, 0, 0))
	startHTML := hl
	startFrag := startHTML + len(pre)
	endFrag := startFrag + len(fragment)
	endHTML := endFrag + len(post)
	return []byte(fmt.Sprintf(hdr, startHTML, endHTML, startFrag, endFrag) + pre + fragment + post + "\x00")
}

// htmlFragment extracts the fragment from CF_HTML data.
func htmlFragment(data []byte) string {
	s := string(data)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	num := func(key string) int {
		i := strings.Index(s, key)
		if i < 0 {
			return -1
		}
		i += len(key)
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		n := 0
		for _, c := range s[i:j] {
			n = n*10 + int(c-'0')
		}
		if j == i {
			return -1
		}
		return n
	}
	a, b := num("StartFragment:"), num("EndFragment:")
	if a >= 0 && b >= a && b <= len(s) {
		return s[a:b]
	}
	a, b = num("StartHTML:"), num("EndHTML:")
	if a >= 0 && b >= a && b <= len(s) {
		return s[a:b]
	}
	return ""
}
