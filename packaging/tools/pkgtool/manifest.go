package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/update"
)

func cmdKeygen(args []string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Println("# Store UPDATE_SIGNING_KEY as a GitHub Actions secret. NEVER commit it.")
	fmt.Println("UPDATE_SIGNING_KEY=" + base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Println("# Embed this public key in the app (internal/update key list):")
	fmt.Println("UPDATE_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(pub))
	return nil
}

// classify maps a release file name to (os, arch, kind). Names follow
// sweef_<version>_<os>_<arch>.<ext> as produced by scripts/release.
func classify(name string) (goos, arch, kind string, ok bool) {
	switch {
	case strings.HasSuffix(name, ".sig"), name == "SHA256SUMS", name == "latest.json":
		return
	}
	switch {
	case strings.HasSuffix(name, ".dmg"):
		return "darwin", "universal", "installer", true
	case strings.HasSuffix(name, "_setup.exe"), strings.HasSuffix(name, ".msi"):
		kind = "installer"
	case strings.HasSuffix(name, ".deb"), strings.HasSuffix(name, ".rpm"), strings.HasSuffix(name, ".AppImage"):
		kind = "installer"
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".zip"):
		kind = "archive"
	default:
		return
	}
	for _, o := range []string{"linux", "windows", "darwin"} {
		for _, a := range []string{"amd64", "arm64", "x86_64", "aarch64", "universal"} {
			if strings.Contains(name, "_"+o+"_"+a) || strings.Contains(name, "-"+o+"-"+a) {
				if a == "x86_64" {
					a = "amd64"
				} else if a == "aarch64" {
					a = "arm64"
				}
				return o, a, kind, true
			}
		}
	}
	return
}

func cmdManifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	out := fs.String("o", "latest.json", "output file")
	version := fs.String("version", "", "release version")
	channel := fs.String("channel", "alpha", "channel")
	base := fs.String("base-url", "", "download URL prefix (release assets directory)")
	sums := fs.String("sums", "SHA256SUMS", "SHA256SUMS file (sha256sum format)")
	notes := fs.String("notes-url", "", "release notes URL")
	days := fs.Int("valid-days", 45, "manifest validity in days")
	keyEnv := fs.String("key-env", "UPDATE_SIGNING_KEY", "env var holding a base64 ed25519 seed")
	epoch := fs.Int64("epoch", 0, "SOURCE_DATE_EPOCH for the published time (default now)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || *base == "" {
		return fmt.Errorf("-version and -base-url are required")
	}
	f, err := os.Open(*sums)
	if err != nil {
		return err
	}
	defer f.Close()
	pub := time.Now().UTC().Truncate(time.Second)
	if *epoch > 0 {
		pub = time.Unix(*epoch, 0).UTC()
	}
	m := update.Manifest{
		Schema: update.SchemaVersion, Channel: *channel, Version: strings.TrimPrefix(*version, "v"),
		Published: pub, Expires: pub.Add(time.Duration(*days) * 24 * time.Hour), NotesURL: *notes,
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		goos, arch, kind, ok := classify(name)
		if !ok {
			continue
		}
		st, err := os.Stat(dirOf(*sums) + name)
		if err != nil {
			return fmt.Errorf("size of %s: %w (run next to the release files)", name, err)
		}
		m.Artifacts = append(m.Artifacts, update.Artifact{
			OS: goos, Arch: arch, Kind: kind, URL: strings.TrimRight(*base, "/") + "/" + name,
			SHA256: strings.ToLower(fields[0]), Size: st.Size(),
		})
	}
	if err := sc.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := m.Validate(); err != nil {
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	seedB64 := os.Getenv(*keyEnv)
	if seedB64 == "" {
		fmt.Fprintf(os.Stderr, "WARNING: %s is not set: %s written UNSIGNED, no %s.sig; auto-update clients will reject it.\n", *keyEnv, *out, *out)
		os.Remove(*out + ".sig")
		return nil
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seedB64))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s must be a base64 ed25519 seed (%d bytes)", *keyEnv, ed25519.SeedSize)
	}
	sig := update.Sign(ed25519.NewKeyFromSeed(seed), data)
	if err := os.WriteFile(*out+".sig", []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "signed %s (%d artifacts, valid %d days)\n", *out, len(m.Artifacts), *days)
	return nil
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[:i+1]
	}
	return ""
}
