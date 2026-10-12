package keymap

import (
	"errors"
	"net/url"
	"strings"
)

// CheckOpenURL validates a string before it is handed to the OS "open"
// facility (xdg-open, ShellExecute, NSWorkspace). Only absolute URLs with a
// real scheme are allowed: bare paths, option-looking strings, single-letter
// "schemes" (Windows drive letters) and schemes that run local content
// (file, javascript, vbscript, data) are rejected so the browser can never be
// tricked into launching programs.
func CheckOpenURL(raw string) (string, error) {
	if raw == "" || len(raw) > 8192 {
		return "", errors.New("shell: invalid URL length")
	}
	if strings.ContainsAny(raw, "\x00\r\n") || strings.TrimSpace(raw) != raw {
		return "", errors.New("shell: URL contains control or surrounding space characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	sc := strings.ToLower(u.Scheme)
	if len(sc) < 2 {
		return "", errors.New("shell: not an absolute URL: " + raw)
	}
	switch sc {
	case "file", "javascript", "vbscript", "data", "about", "blob":
		return "", errors.New("shell: refusing to open " + sc + ": URL")
	}
	return raw, nil
}
