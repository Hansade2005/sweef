package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var errIncomplete = errors.New("update: download incomplete")

// MaxManifestBytes bounds manifest and signature downloads.
const MaxManifestBytes = 1 << 20

// Fetch GETs a small document (manifest or signature) with a size cap.
func Fetch(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxManifestBytes {
		return nil, fmt.Errorf("update: GET %s: response exceeds %d bytes", url, MaxManifestBytes)
	}
	return b, nil
}

// Check downloads manifestURL and manifestURL+".sig", verifies the signature,
// and returns the manifest only when it is newer than currentVersion.
// It returns ErrNotNewer when already up to date.
func Check(ctx context.Context, c *http.Client, manifestURL string, keys []ed25519.PublicKey, currentVersion string, now func() time.Time) (*Manifest, error) {
	data, err := Fetch(ctx, c, manifestURL)
	if err != nil {
		return nil, err
	}
	sig, err := Fetch(ctx, c, manifestURL+".sig")
	if err != nil {
		return nil, err
	}
	m, err := VerifyAndParse(data, string(sig), keys, now())
	if err != nil {
		return nil, err
	}
	if !IsNewer(m.Version, currentVersion) {
		return m, ErrNotNewer
	}
	return m, nil
}

// Download fetches a.URL into dest, resuming a previous partial download
// (dest+".part") with an HTTP Range request when possible. The finished file
// is size- and SHA-256-verified against the (already signature-verified)
// artifact before being renamed to dest. On checksum failure the partial file
// is deleted so the next attempt starts clean.
func Download(ctx context.Context, c *http.Client, a Artifact, dest string) error {
	if c == nil {
		c = http.DefaultClient
	}
	part := dest + ".part"
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
		if offset > a.Size {
			os.Remove(part)
			offset = 0
		}
	}
	if offset < a.Size { // otherwise: the part file is complete, only verify
		if err := fetchInto(ctx, c, a, part, offset); err != nil {
			return err
		}
	}
	if err := verifyFile(part, a); err != nil {
		if !errors.Is(err, errIncomplete) {
			os.Remove(part) // corrupt or oversized: never resume from bad bytes
		}
		return err
	}
	return os.Rename(part, dest)
}

func fetchInto(ctx context.Context, c *http.Client, a Artifact, part string, offset int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC // server ignored Range (or fresh download): start over
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return fmt.Errorf("update: GET %s: range not satisfiable; partial file discarded, retry", a.URL)
	default:
		return fmt.Errorf("update: GET %s: %s", a.URL, resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	// Never write more than the signed size.
	max := a.Size + 1
	if flags&os.O_APPEND != 0 {
		max = a.Size - offset + 1
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, max))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err // a short read leaves the .part file for the next resume
}

func verifyFile(path string, a Artifact) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != a.Size {
		if n < a.Size {
			return fmt.Errorf("%w (%d of %d bytes); call Download again to resume", errIncomplete, n, a.Size)
		}
		return fmt.Errorf("%w: size %d, want %d", ErrChecksum, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("%w: got %s", ErrChecksum, got)
	}
	return nil
}
