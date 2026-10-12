//go:build !windows

package agentbridge

import "strings"

// protect/unprotect: on Unix the protection is the file mode (0600 in a 0700
// directory); the file holds "v1\n<key>".
func protect(key string) ([]byte, error) { return []byte("v1\n" + key + "\n"), nil }

func unprotect(b []byte) (string, error) {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "v1\n")
	return strings.TrimSpace(s), nil
}
