package agentbridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// KeyStore keeps the user's Anthropic API key.
//
// Where it lives:
//   - Windows: a DPAPI blob (CryptProtectData, bound to the user account) in a
//     0600 file under the profile directory.
//   - macOS and Linux: a 0600 file under the profile directory (the directory
//     itself is 0700). There is deliberately no `security add-generic-password
//     -w` call, because that puts the key in the process arguments. A native
//     Keychain / Secret Service backend is future work; see docs/agent.md.
//   - No profile directory (in-memory / incognito / headless): memory only.
//
// The ANTHROPIC_API_KEY environment variable is a fallback for development;
// it is never written anywhere. The key is never logged, never sent to the
// chrome page and is redacted from every error that leaves this package.
type KeyStore struct {
	mu   sync.Mutex
	dir  string
	mem  string
	env  func(string) string
	noEn bool
}

const keyFile = "agent.key"

// NewKeyStore returns a store rooted at dir ("" = memory only). env is the
// environment lookup (nil = os.Getenv); useEnv enables the env fallback.
func NewKeyStore(dir string, env func(string) string, useEnv bool) *KeyStore {
	if env == nil {
		env = os.Getenv
	}
	return &KeyStore{dir: dir, env: env, noEn: !useEnv}
}

// SetDir changes the directory (used once the profile is known). A key saved
// in memory before that is persisted.
func (k *KeyStore) SetDir(dir string) {
	k.mu.Lock()
	k.dir = dir
	mem := k.mem
	k.mu.Unlock()
	if mem != "" && dir != "" {
		_ = k.Save(mem)
	}
}

// ValidKey reports whether s looks like an API key (no whitespace or control
// characters, plausible length). It does not check the key with the server.
func ValidKey(s string) bool {
	if len(s) < 16 || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Get returns the key and where it came from ("session", "file", "env", "").
func (k *KeyStore) Get() (key, source string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.mem != "" {
		return k.mem, "session"
	}
	if k.dir != "" {
		if b, err := os.ReadFile(filepath.Join(k.dir, keyFile)); err == nil {
			if s, err := unprotect(b); err == nil && ValidKey(s) {
				return s, "file"
			}
		}
	}
	if !k.noEn {
		if v := strings.TrimSpace(k.env("ANTHROPIC_API_KEY")); ValidKey(v) {
			return v, "env"
		}
	}
	return "", ""
}

// Save stores the key (file if a directory is known, always in memory).
func (k *KeyStore) Save(key string) error {
	key = strings.TrimSpace(key)
	if !ValidKey(key) {
		return errors.New("that does not look like an API key")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.mem = key
	if k.dir == "" {
		return nil
	}
	blob, err := protect(key)
	if err != nil {
		return errors.New("could not protect the key for storage")
	}
	if err := os.MkdirAll(k.dir, 0o700); err != nil {
		return errors.New("could not create the profile directory")
	}
	tmp, err := os.CreateTemp(k.dir, ".agent.key.*")
	if err != nil {
		return errors.New("could not write the key file")
	}
	name := tmp.Name()
	_ = tmp.Chmod(0o600)
	_, werr := tmp.Write(blob)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return errors.New("could not write the key file")
	}
	if err := os.Rename(name, filepath.Join(k.dir, keyFile)); err != nil {
		_ = os.Remove(name)
		return errors.New("could not write the key file")
	}
	return nil
}

// Clear forgets the stored key (memory and file). An env key stays.
func (k *KeyStore) Clear() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.mem = ""
	if k.dir != "" {
		_ = os.Remove(filepath.Join(k.dir, keyFile))
	}
}

// redact removes the key from s.
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[REDACTED]")
}
