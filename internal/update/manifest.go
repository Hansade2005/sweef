// Package update implements the verification side of the Sweef auto-updater:
// a signed release manifest (ed25519), resumable checksum-verified downloads,
// and a staged binary replacement with rollback. It is deliberately pure Go,
// has no dependency on the rest of Sweef, and is not wired into the CLI yet.
// The design is described in docs/updates.md.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is the only manifest schema this code understands.
const SchemaVersion = 1

// Artifact is one downloadable file listed in a manifest.
type Artifact struct {
	OS     string `json:"os"`   // GOOS: linux, darwin, windows
	Arch   string `json:"arch"` // GOARCH: amd64, arm64, or "universal" on darwin
	Kind   string `json:"kind"` // "binary" (raw executable, used by the updater), "archive", "installer"
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // lowercase hex
	Size   int64  `json:"size"`
}

// Manifest is the document published as latest.json. It is signed as exact
// bytes: the detached signature (latest.json.sig, base64) covers the file
// content verbatim, so there is no canonicalisation to get wrong.
type Manifest struct {
	Schema    int        `json:"schema"`
	Channel   string     `json:"channel"` // e.g. "alpha"
	Version   string     `json:"version"` // e.g. "0.1.0-alpha.7"
	Published time.Time  `json:"published"`
	Expires   time.Time  `json:"expires"` // freeze-attack protection
	NotesURL  string     `json:"notes_url,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// Errors returned by this package.
var (
	ErrBadSignature = errors.New("update: manifest signature verification failed")
	ErrExpired      = errors.New("update: manifest has expired")
	ErrNoArtifact   = errors.New("update: no artifact for this platform")
	ErrNotNewer     = errors.New("update: manifest version is not newer than the running version")
	ErrChecksum     = errors.New("update: sha256 mismatch")
)

// ParsePublicKey decodes a base64 (std) ed25519 public key.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("update: public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("update: public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// Sign returns the base64 detached signature of data.
func Sign(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

// Verify checks sigB64 over data against any of keys (several keys allow
// rotation: ship the new key in a release before retiring the old one).
func Verify(data []byte, sigB64 string, keys []ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, data, sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// Parse decodes and validates a manifest. It does NOT check the signature;
// callers must call Verify on the same bytes first (see VerifyAndParse).
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("update: manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// VerifyAndParse verifies the signature, then parses, then checks expiry.
func VerifyAndParse(data []byte, sigB64 string, keys []ed25519.PublicKey, now time.Time) (*Manifest, error) {
	if err := Verify(data, sigB64, keys); err != nil {
		return nil, err
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if !now.Before(m.Expires) {
		return nil, ErrExpired
	}
	return m, nil
}

// Validate checks structural invariants.
func (m *Manifest) Validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("update: unsupported manifest schema %d", m.Schema)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return err
	}
	if m.Channel == "" {
		return errors.New("update: manifest has no channel")
	}
	if m.Expires.IsZero() {
		return errors.New("update: manifest has no expiry")
	}
	if len(m.Artifacts) == 0 {
		return errors.New("update: manifest lists no artifacts")
	}
	for i, a := range m.Artifacts {
		if a.OS == "" || a.Arch == "" || a.Kind == "" {
			return fmt.Errorf("update: artifact %d: os/arch/kind required", i)
		}
		u, err := url.Parse(a.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("update: artifact %d: bad url %q", i, a.URL)
		}
		if raw, err := hex.DecodeString(a.SHA256); err != nil || len(raw) != 32 || a.SHA256 != strings.ToLower(a.SHA256) {
			return fmt.Errorf("update: artifact %d: sha256 must be 64 lowercase hex chars", i)
		}
		if a.Size <= 0 {
			return fmt.Errorf("update: artifact %d: size must be positive", i)
		}
	}
	return nil
}

// Select returns the artifact of the given kind for the platform. On darwin a
// "universal" artifact matches any arch.
func (m *Manifest) Select(goos, goarch, kind string) (Artifact, error) {
	for _, a := range m.Artifacts {
		if a.OS == goos && a.Kind == kind && (a.Arch == goarch || (goos == "darwin" && a.Arch == "universal")) {
			return a, nil
		}
	}
	return Artifact{}, ErrNoArtifact
}

// Version is a parsed semantic version (no build metadata).
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // dot-separated pre-release identifiers
}

// ParseVersion parses "1.2.3" or "1.2.3-alpha.4" (a leading "v" is allowed).
func ParseVersion(s string) (Version, error) {
	orig := s
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("update: bad version %q", orig)
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, fmt.Errorf("update: bad version %q", orig)
		}
		*nums[i] = n
	}
	if hasPre {
		if pre == "" {
			return v, fmt.Errorf("update: bad version %q", orig)
		}
		v.Pre = strings.Split(pre, ".")
	}
	return v, nil
}

// Compare returns -1, 0 or 1 following semver precedence rules.
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1 // a release outranks its pre-releases
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, b := v.Pre[i], o.Pre[i]
		an, aerr := strconv.Atoi(a)
		bn, berr := strconv.Atoi(b)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aerr == nil:
			return -1 // numeric identifiers sort before alphanumeric
		case berr == nil:
			return 1
		default:
			if a != b {
				if a < b {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

// IsNewer reports whether candidate outranks current. Unparseable versions
// are never considered newer (fail closed).
func IsNewer(candidate, current string) bool {
	c, err1 := ParseVersion(candidate)
	r, err2 := ParseVersion(current)
	return err1 == nil && err2 == nil && c.Compare(r) > 0
}
