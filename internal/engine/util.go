package engine

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Dialog models a JS dialog (alert/confirm/prompt/beforeunload) the way
// Playwright surfaces page.on("dialog"). The engine records it in a ring and
// applies whatever response an agent armed beforehand.
type Dialog struct {
	Kind    string `json:"kind"` // alert | confirm | prompt | beforeunload
	Message string `json:"message"`
	URL     string `json:"url,omitempty"`
}

func (d Dialog) String() string { return fmt.Sprintf("%s: %s", d.Kind, d.Message) }

// hostOf extracts the hostname from a URL string ("" on failure).
func hostOf(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// readCapped drains r up to n bytes without ever buffering more than n.
func readCapped(r interface{ Read([]byte) (int, error) }, n int64) []byte {
	lr := &io_LimitReader{R: r, N: n}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		m, err := lr.Read(tmp)
		buf = append(buf, tmp[:m]...)
		if err != nil || int64(len(buf)) >= n {
			break
		}
	}
	return buf
}

// io_LimitReader mirrors io.LimitReader (kept local to avoid import cycle noise
// in this helper file; identical semantics).
type io_LimitReader struct {
	R interface{ Read([]byte) (int, error) }
	N int64
}

func (l *io_LimitReader) Read(p []byte) (int, error) {
	if l.N <= 0 {
		return 0, fmt.Errorf("EOF")
	}
	if int64(len(p)) > l.N {
		p = p[:l.N]
	}
	n, err := l.R.Read(p)
	l.N -= int64(n)
	return n, err
}

// parseCookie converts a raw Set-Cookie header into our Cookie record.
// Implements the subset of RFC-6265 that matters for agents: expires/max-age,
// domain, path, httpOnly, secure, sameSite.
func parseCookie(raw string, now time.Time) *Cookie {
	parts := strings.Split(raw, ";")
	if len(parts) == 0 {
		return nil
	}
	kv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
	if len(kv) != 2 {
		return nil
	}
	c := &Cookie{Name: kv[0], Value: strings.Trim(kv[1], `"`), Creation: now.Unix(), Path: "/"}
	for _, attrS := range parts[1:] {
		attrS = strings.TrimSpace(attrS)
		lower := strings.ToLower(attrS)
		switch {
		case lower == "httponly":
			c.HttpOnly = true
		case lower == "secure":
			c.Secure = true
		case strings.HasPrefix(lower, "max-age="):
			var secs int64
			if _, err := fmt.Sscanf(strings.TrimPrefix(lower, "max-age="), "%d", &secs); err == nil {
				if secs <= 0 {
					c.Expires = 1 // expire immediately
				} else {
					c.Expires = now.Unix() + secs
				}
			}
		case strings.HasPrefix(lower, "expires="):
			if t, err := time.Parse(time.RFC1123, strings.TrimPrefix(attrS, "Expires=")); err == nil {
				c.Expires = t.Unix()
			} else if t, err := time.Parse("Mon, 02-Jan-2006 15:04:05 GMT", strings.TrimPrefix(attrS, "expires=")); err == nil {
				c.Expires = t.Unix()
			}
		case strings.HasPrefix(lower, "domain="):
			c.Domain = strings.TrimPrefix(lower, "domain=")
		case strings.HasPrefix(lower, "path="):
			c.Path = strings.TrimPrefix(lower, "path=")
		case strings.HasPrefix(lower, "samesite="):
			switch strings.TrimPrefix(lower, "samesite=") {
			case "strict":
				c.SameSite = 1
			case "lax":
				c.SameSite = 2
			case "none":
				c.SameSite = 3
			}
		}
	}
	return c
}
