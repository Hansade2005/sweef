package cdm

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testManifest = `{
  "manifest_version": 2,
  "name": "WidevineCdm",
  "version": "%VER%",
  "x-cdm-codecs": "vp8,vp9.0,vp9.2,avc1,av01",
  "x-cdm-host-versions": "10",
  "x-cdm-interface-versions": "10,11",
  "x-cdm-module-versions": "4",
  "x-cdm-persistent-license-support": false,
  "x-cdm-supported-encryption-schemes": ["cenc", "cbcs"]
}`

// writeVersion lays out a Chrome-style component directory: <root>/<ver>/manifest.json and the library below
// _platform_specific/<plat>/.
func writeVersion(t *testing.T, root, ver, goos, goarch string) string {
	t.Helper()
	dir := filepath.Join(root, ver)
	lib := filepath.Join(dir, "_platform_specific", WidevinePlatformDir(goos, goarch), WidevineLibraryName(goos))
	if err := os.MkdirAll(filepath.Dir(lib), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lib, []byte("not a real CDM"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(strings.ReplaceAll(testManifest, "%VER%", ver)), 0o644); err != nil {
		t.Fatal(err)
	}
	return lib
}

func TestDiscoverChromeInstallationsPerPlatform(t *testing.T) {
	cases := []struct {
		goos, goarch string
		root         func(home string) string
		env          map[string]string
		libName      string
	}{
		{"linux", "amd64", func(h string) string { return filepath.Join(h, ".config", "google-chrome", "WidevineCdm") }, nil, "libwidevinecdm.so"},
		{"linux", "arm64", func(h string) string { return filepath.Join(h, ".config", "google-chrome", "WidevineCdm") }, nil, "libwidevinecdm.so"},
		{"darwin", "arm64", func(h string) string {
			return filepath.Join(h, "Library", "Application Support", "Google", "Chrome", "WidevineCdm")
		}, nil, "libwidevinecdm.dylib"},
		{"darwin", "amd64", func(h string) string {
			return filepath.Join(h, "Library", "Application Support", "Google", "Chrome", "WidevineCdm")
		}, nil, "libwidevinecdm.dylib"},
		{"windows", "amd64", func(h string) string {
			return filepath.Join(h, "Local", "Google", "Chrome", "User Data", "WidevineCdm")
		},
			map[string]string{"LOCALAPPDATA": "@HOME/Local"}, "widevinecdm.dll"},
	}
	for _, c := range cases {
		t.Run(c.goos+"_"+c.goarch, func(t *testing.T) {
			home := t.TempDir()
			root := c.root(home)
			writeVersion(t, root, "4.10.2557.0", c.goos, c.goarch)
			want := writeVersion(t, root, "4.10.2710.0", c.goos, c.goarch)
			writeVersion(t, root, "4.9.9999.0", c.goos, c.goarch) // lower numerically although it sorts higher as text
			env := map[string]string{}
			for k, v := range c.env {
				env[k] = strings.ReplaceAll(v, "@HOME", home)
			}
			in, err := DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: c.goos, GOARCH: c.goarch, Home: home, Getenv: func(k string) string { return env[k] }})
			if err != nil {
				t.Fatal(err)
			}
			if in.Library != want || in.Version != "4.10.2710.0" || in.Source != "Google Chrome" || filepath.Base(in.Library) != c.libName {
				t.Fatalf("%+v", in)
			}
			if !reflect.DeepEqual(in.Manifest.InterfaceVersions, []int{10, 11}) || !reflect.DeepEqual(in.Manifest.ModuleVersions, []int{4}) {
				t.Fatalf("manifest %+v", in.Manifest)
			}
		})
	}
}

func TestDiscoverNothingFound(t *testing.T) {
	home := t.TempDir()
	_, err := DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: func(string) string { return "" }})
	if !errors.Is(err, ErrWidevineNotFound) || !strings.Contains(err.Error(), WidevineEnv) {
		t.Fatalf("%v", err)
	}
	_, err = DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "freebsd", GOARCH: "amd64", Home: home, Getenv: func(string) string { return "" }})
	if !errors.Is(err, ErrWidevineNotFound) {
		t.Fatalf("%v", err)
	}
	// the wrong platform directory is not picked up
	root := filepath.Join(home, ".config", "google-chrome", "WidevineCdm")
	writeVersion(t, root, "4.10.1.0", "linux", "arm64")
	if _, err := DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: func(string) string { return "" }}); !errors.Is(err, ErrWidevineNotFound) {
		t.Fatalf("an arm64 CDM must not be used on amd64: %v", err)
	}
}

func TestDiscoverExplicitPathAndEnv(t *testing.T) {
	home := t.TempDir()
	lib := writeVersion(t, filepath.Join(home, "elsewhere"), "5.0.0.1", "linux", "amd64")
	// also a Chrome install that must lose against the explicit configuration
	writeVersion(t, filepath.Join(home, ".config", "google-chrome", "WidevineCdm"), "9.9.9.9", "linux", "amd64")
	noEnv := func(string) string { return "" }

	// settings: a library file
	in, err := DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: noEnv, Path: lib})
	if err != nil || in.Library != lib || in.Version != "5.0.0.1" || in.Source != "settings" || in.Manifest.Name != "WidevineCdm" {
		t.Fatalf("%+v %v", in, err)
	}
	// env: a directory holding versions
	env := func(k string) string {
		if k == WidevineEnv {
			return filepath.Join(home, "elsewhere")
		}
		return ""
	}
	in, err = DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: env})
	if err != nil || in.Library != lib || in.Source != WidevineEnv {
		t.Fatalf("%+v %v", in, err)
	}
	// settings beat the environment
	other := writeVersion(t, filepath.Join(home, "other"), "6.0.0.0", "linux", "amd64")
	in, err = DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: env, Path: other})
	if err != nil || in.Library != other {
		t.Fatalf("%+v %v", in, err)
	}
	// a bare library without a manifest still works
	bare := filepath.Join(home, "bare", "libwidevinecdm.so")
	_ = os.MkdirAll(filepath.Dir(bare), 0o755)
	_ = os.WriteFile(bare, []byte("x"), 0o755)
	in, err = DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: noEnv, Path: bare})
	if err != nil || in.Library != bare || in.Version != "" || len(in.Manifest.Codecs) != 0 {
		t.Fatalf("%+v %v", in, err)
	}
	// a wrong explicit path is an error, not a silent fallback to Chrome's copy
	for _, p := range []string{filepath.Join(home, "missing"), t.TempDir()} {
		if _, err := DiscoverWidevine(DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: noEnv, Path: p}); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
}

func TestParseWidevineManifest(t *testing.T) {
	m, err := ParseWidevineManifest([]byte(`{"name":"x","version":"1.2","x-cdm-codecs":["vp8","AVC1"],"x-cdm-persistent-license-support":true,
		"x-cdm-interface-versions":11,"x-cdm-module-versions":[4],"x-cdm-supported-encryption-schemes":"cenc,cbcs"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Codecs, []string{"vp8", "avc1"}) || !m.PersistentLicense || !reflect.DeepEqual(m.InterfaceVersions, []int{11}) ||
		!reflect.DeepEqual(m.ModuleVersions, []int{4}) || !reflect.DeepEqual(m.Schemes, []Scheme{"cenc", "cbcs"}) {
		t.Fatalf("%+v", m)
	}
	if _, err := ParseWidevineManifest([]byte("{")); err == nil {
		t.Fatal("bad json accepted")
	}
	if compareVersions("4.10.0.0", "4.9.0.0") <= 0 || compareVersions("1.2", "1.2.0") != 0 || compareVersions("1", "2") >= 0 {
		t.Fatal("compareVersions")
	}
}

func TestWidevineProviderAvailabilityAndCapabilities(t *testing.T) {
	home := t.TempDir()
	noEnv := func(string) string { return "" }
	disc := DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: noEnv}

	// nothing installed: unsupported, with an actionable reason, and nothing is started
	p := NewWidevineProvider(WidevineOptions{Discover: disc, Connect: func(context.Context, *WidevineInstall, InitParams) (*Remote, error) {
		t.Fatal("must not connect")
		return nil, nil
	}})
	av := p.Availability()
	if av.Supported || !strings.Contains(av.Reason, WidevineEnv) {
		t.Fatalf("%+v", av)
	}
	if _, err := p.New(context.Background(), InstanceConfig{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("New without CDM: %v", err)
	}
	r := NewRegistry()
	r.Register(p)
	if _, err := r.Negotiate(WidevineKeySystem, []KeySystemConfig{{VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="avc1.64001f"`, "")}}}); err == nil {
		t.Fatal("Negotiate must fail without a CDM")
	}

	// install one: capabilities come from the manifest
	writeVersion(t, filepath.Join(home, ".config", "google-chrome", "WidevineCdm"), "4.10.2710.0", "linux", "amd64")
	RefreshProvider(p)
	if av := p.Availability(); !av.Supported {
		t.Fatalf("%+v", av)
	}
	caps := p.Capabilities()
	if !reflect.DeepEqual(caps.VideoCodecs, []string{"vp8", "vp9", "avc1", "av01"}) || caps.SupportsPersistentLicense() ||
		!reflect.DeepEqual(caps.Schemes, []Scheme{SchemeCenc, SchemeCbcs}) || caps.Version != "4.10.2710.0" || caps.Mode != ModeDecrypt {
		t.Fatalf("%+v", caps)
	}
	if caps.MaxHeight("SW_SECURE_CRYPTO") != 720 {
		t.Fatal("L3 desktop CDM is documented as 720p")
	}
	for _, l := range caps.Robustness {
		if l.Hardware {
			t.Fatalf("the software CDM must not advertise hardware robustness: %+v", l)
		}
	}
	if in := WidevineInstallOf(p); in == nil || in.Version != "4.10.2710.0" {
		t.Fatalf("%+v", in)
	}

	// negotiation honours the manifest and the robustness list
	if _, err := r.Negotiate(WidevineKeySystem, []KeySystemConfig{{
		InitDataTypes:     []string{"cenc"},
		VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="avc1.640028"`, "SW_SECURE_CRYPTO")},
		AudioCapabilities: []MediaCapability{mc(`audio/mp4; codecs="mp4a.40.2"`, "SW_SECURE_CRYPTO")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Negotiate(WidevineKeySystem, []KeySystemConfig{{VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="avc1.640028"`, "HW_SECURE_ALL")}}}); err == nil {
		t.Fatal("HW_SECURE_ALL must be refused by the software CDM")
	}
	if _, err := r.Negotiate(WidevineKeySystem, []KeySystemConfig{{VideoCapabilities: []MediaCapability{mc(`video/mp4; codecs="hvc1.1.6.L93.B0"`, "")}}}); err == nil {
		t.Fatal("HEVC is not in this manifest")
	}
}

func TestWidevineNewPassesStorageAndOrigin(t *testing.T) {
	home := t.TempDir()
	lib := writeVersion(t, filepath.Join(home, "cdm"), "4.10.1.0", "linux", "amd64")
	var got InitParams
	var gotIn *WidevineInstall
	p := NewWidevineProvider(WidevineOptions{
		Path:     lib,
		Discover: DiscoverOptions{SystemRoot: noSystem(t), GOOS: "linux", GOARCH: "amd64", Home: home, Getenv: func(string) string { return "" }},
		Connect: func(ctx context.Context, in *WidevineInstall, init InitParams) (*Remote, error) {
			got, gotIn = init, in
			c, s := net.Pipe()
			go Serve(ctx, s, newBackendEngine())
			return NewRemote(ctx, c, init, nil)
		},
	})
	b, err := p.New(context.Background(), InstanceConfig{Origin: "https://netflix.example", StorageDir: "/var/sweef/cdm", HardwareSecure: false})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if gotIn == nil || gotIn.Library != lib {
		t.Fatalf("%+v", gotIn)
	}
	if !got.PersistentState || got.DistinctiveIdentifier || got.Origin != "https://netflix.example" {
		t.Fatalf("%+v", got)
	}
	if !strings.HasPrefix(got.StorageDir, filepath.Join("/var/sweef/cdm", "widevine")) || strings.Contains(got.StorageDir, "netflix") {
		t.Fatalf("storage dir %q must be per origin and must not embed the origin text", got.StorageDir)
	}
	if originDirName("https://a.example") == originDirName("https://b.example") || originDirName("") != "default" {
		t.Fatal("originDirName")
	}
	// without a storage dir nothing is persisted
	b2, err := p.New(context.Background(), InstanceConfig{Origin: "https://x"})
	if err != nil {
		t.Fatal(err)
	}
	b2.Close()
	if got.PersistentState || got.StorageDir != "" {
		t.Fatalf("%+v", got)
	}
}

func TestHelperLookup(t *testing.T) {
	dir := t.TempDir()
	h := filepath.Join(dir, HelperName())
	if err := os.WriteFile(h, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(HelperEnv, h)
	got, err := FindHelper()
	if err != nil || got != h {
		t.Fatalf("%q %v", got, err)
	}
	if HelperSignaturePath(h) != h+".sig" {
		t.Fatal("signature path")
	}
	t.Setenv(HelperEnv, filepath.Join(dir, "nope"))
	if _, err := FindHelper(); err == nil {
		t.Fatal("missing helper accepted")
	}
}

// noSystem returns an empty directory used as the system root, so tests never see a CDM installed on the machine
// running them (CI runners ship Google Chrome).
func noSystem(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
