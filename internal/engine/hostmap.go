package engine

import (
	"net"
	"strings"
	"sync"
)

// hostmap.go: a resolver override. SetHostMap makes the engine's dialer connect to a fixed address for the
// listed host names, whatever DNS says, so tests (and embedders that front a private network) can use realistic
// distinct sites such as "a.localhost" and "b.localhost" against one local server. The map is process-wide,
// matched case-insensitively on the host part, and is empty by default; with it empty the dialer is unchanged.

var hostMap struct {
	mu sync.RWMutex
	m  map[string]string
}

// SetHostMap replaces the override table (host name -> IP address or host). Pass nil to clear it.
func SetHostMap(m map[string]string) {
	n := make(map[string]string, len(m))
	for k, v := range m {
		n[strings.ToLower(strings.TrimSuffix(k, "."))] = v
	}
	hostMap.mu.Lock()
	hostMap.m = n
	hostMap.mu.Unlock()
}

// mapDialAddr rewrites host:port when the host is in the override table.
func mapDialAddr(addr string) string {
	hostMap.mu.RLock()
	m := hostMap.m
	hostMap.mu.RUnlock()
	if len(m) == 0 {
		return addr
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if to, ok := m[strings.ToLower(h)]; ok {
		return net.JoinHostPort(to, p)
	}
	return addr
}
