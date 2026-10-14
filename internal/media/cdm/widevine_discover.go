package cdm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// WidevineEnv names the environment variable that points Sweef at a Widevine CDM library (or at a directory that
// holds one). Settings can pass the same value as WidevineOptions.Path.
const WidevineEnv = "SWEEF_WIDEVINE_CDM"

// WidevineKeySystem is the Widevine key system string.
const WidevineKeySystem = "com.widevine.alpha"

// WidevineManifest is the part of a Widevine CDM component's manifest.json that Sweef reads.
type WidevineManifest struct {
	Name              string
	Version           string
	Codecs            []string // raw "x-cdm-codecs" entries: "vp8", "vp9.0", "avc1", "av01"
	PersistentLicense bool
	Schemes           []Scheme
	ModuleVersions    []int // "x-cdm-module-versions", normally [4]
	InterfaceVersions []int // "x-cdm-interface-versions", normally [10] or [11]
	HostVersions      []int // "x-cdm-host-versions"
}

// WidevineInstall is a Widevine CDM that was found on this machine. Sweef never ships or downloads one.
type WidevineInstall struct {
	Library  string // absolute path of libwidevinecdm.so / widevinecdm.dll / libwidevinecdm.dylib
	Dir      string // the version directory (holds manifest.json), may be empty when there is none
	Version  string
	Source   string // human readable origin: "SWEEF_WIDEVINE_CDM", "Google Chrome", ...
	Manifest WidevineManifest
}

// DiscoverOptions controls the search; the zero value searches this machine. The fields exist for tests.
type DiscoverOptions struct {
	// Path is an explicit library file or directory (settings). It wins over the environment.
	Path   string
	Getenv func(string) string
	GOOS   string
	GOARCH string
	Home   string
}

func (o *DiscoverOptions) fill() {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if o.Home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			o.Home = h
		}
	}
}

// ErrWidevineNotFound means no explicit path was given and no installation was found.
var ErrWidevineNotFound = errors.New("no Widevine CDM found")

// WidevineLibraryName is the file name of the CDM library on goos.
func WidevineLibraryName(goos string) string {
	switch goos {
	case "windows":
		return "widevinecdm.dll"
	case "darwin":
		return "libwidevinecdm.dylib"
	}
	return "libwidevinecdm.so"
}

// WidevinePlatformDir is the _platform_specific subdirectory name Chrome's component layout uses
// ("linux_x64", "mac_arm64", "win_x64"), or "" for unsupported combinations.
func WidevinePlatformDir(goos, goarch string) string {
	var os_, arch string
	switch goos {
	case "linux":
		os_ = "linux"
	case "darwin":
		os_ = "mac"
	case "windows":
		os_ = "win"
	default:
		return ""
	}
	switch goarch {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	case "386":
		if goos == "windows" {
			arch = "x86"
		}
	}
	if arch == "" {
		return ""
	}
	return os_ + "_" + arch
}

// DiscoverWidevine looks for a Widevine CDM the user already has. Order: the explicit path (settings), then
// $SWEEF_WIDEVINE_CDM, then the WidevineCdm component directories of Google Chrome (and Chromium and Edge, which
// use the same layout). It returns an error wrapping ErrWidevineNotFound when nothing is found. An explicit path
// that is wrong is an error of its own: Sweef does not silently use a different library than the one configured.
func DiscoverWidevine(o DiscoverOptions) (*WidevineInstall, error) {
	o.fill()
	explicit, source := o.Path, "settings"
	if explicit == "" {
		explicit, source = o.Getenv(WidevineEnv), WidevineEnv
	}
	if explicit != "" {
		in, err := installFromPath(explicit, o)
		if err != nil {
			return nil, fmt.Errorf("%s=%q: %w", source, explicit, err)
		}
		in.Source = source
		return in, nil
	}
	if WidevinePlatformDir(o.GOOS, o.GOARCH) == "" {
		return nil, fmt.Errorf("%w: Widevine has no CDM for %s/%s", ErrWidevineNotFound, o.GOOS, o.GOARCH)
	}
	var best *WidevineInstall
	for _, root := range chromeRoots(o) {
		in := bestInRoot(root.dir, o)
		if in == nil {
			continue
		}
		in.Source = root.name
		if best == nil || compareVersions(in.Version, best.Version) > 0 {
			best = in
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: install Google Chrome once (it downloads the CDM), or set %s to libwidevinecdm", ErrWidevineNotFound, WidevineEnv)
	}
	return best, nil
}

type namedRoot struct{ name, dir string }

func chromeRoots(o DiscoverOptions) []namedRoot {
	var roots []namedRoot
	add := func(name, dir string) {
		if dir != "" {
			roots = append(roots, namedRoot{name, dir})
		}
	}
	glob := func(name, pattern string) {
		m, _ := filepath.Glob(pattern)
		sort.Strings(m)
		for _, d := range m {
			add(name, d)
		}
	}
	switch o.GOOS {
	case "linux":
		cfg := o.Getenv("XDG_CONFIG_HOME")
		if cfg == "" && o.Home != "" {
			cfg = filepath.Join(o.Home, ".config")
		}
		if cfg != "" {
			add("Google Chrome", filepath.Join(cfg, "google-chrome", "WidevineCdm"))
			add("Google Chrome Beta", filepath.Join(cfg, "google-chrome-beta", "WidevineCdm"))
			add("Google Chrome Unstable", filepath.Join(cfg, "google-chrome-unstable", "WidevineCdm"))
			add("Chromium", filepath.Join(cfg, "chromium", "WidevineCdm"))
			add("Microsoft Edge", filepath.Join(cfg, "microsoft-edge", "WidevineCdm"))
		}
		add("Google Chrome (system)", "/opt/google/chrome/WidevineCdm")
		add("Google Chrome Beta (system)", "/opt/google/chrome-beta/WidevineCdm")
		add("Microsoft Edge (system)", "/opt/microsoft/msedge/WidevineCdm")
	case "darwin":
		if o.Home != "" {
			app := filepath.Join(o.Home, "Library", "Application Support")
			add("Google Chrome", filepath.Join(app, "Google", "Chrome", "WidevineCdm"))
			add("Chromium", filepath.Join(app, "Chromium", "WidevineCdm"))
			add("Microsoft Edge", filepath.Join(app, "Microsoft Edge", "WidevineCdm"))
		}
		glob("Google Chrome (app bundle)", "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/*/Libraries/WidevineCdm")
	case "windows":
		local := o.Getenv("LOCALAPPDATA")
		if local == "" && o.Home != "" {
			local = filepath.Join(o.Home, "AppData", "Local")
		}
		if local != "" {
			add("Google Chrome", filepath.Join(local, "Google", "Chrome", "User Data", "WidevineCdm"))
			add("Microsoft Edge", filepath.Join(local, "Microsoft", "Edge", "User Data", "WidevineCdm"))
		}
		for _, pf := range []string{o.Getenv("ProgramFiles"), o.Getenv("ProgramFiles(x86)"), o.Getenv("ProgramW6432")} {
			if pf != "" {
				glob("Google Chrome (system)", filepath.Join(pf, "Google", "Chrome", "Application", "*", "WidevineCdm"))
			}
		}
	}
	return roots
}

// bestInRoot picks the highest version directory under a WidevineCdm root that holds a library for this platform.
// The root itself may also be one version directory (app bundle installs).
func bestInRoot(root string, o DiscoverOptions) *WidevineInstall {
	if in := installFromVersionDir(root, o); in != nil {
		return in
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var best *WidevineInstall
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		in := installFromVersionDir(filepath.Join(root, e.Name()), o)
		if in == nil {
			continue
		}
		if best == nil || compareVersions(in.Version, best.Version) > 0 {
			best = in
		}
	}
	return best
}

// installFromVersionDir reads <dir>/manifest.json and finds the library at the Chrome component layout.
func installFromVersionDir(dir string, o DiscoverOptions) *WidevineInstall {
	lib := libraryInDir(dir, o)
	if lib == "" {
		return nil
	}
	in := &WidevineInstall{Library: lib, Dir: dir}
	if m, err := readWidevineManifest(filepath.Join(dir, "manifest.json")); err == nil {
		in.Manifest = *m
		in.Version = m.Version
	}
	if in.Version == "" {
		in.Version = filepath.Base(dir)
	}
	return in
}

func libraryInDir(dir string, o DiscoverOptions) string {
	name := WidevineLibraryName(o.GOOS)
	var cands []string
	if p := WidevinePlatformDir(o.GOOS, o.GOARCH); p != "" {
		cands = append(cands, filepath.Join(dir, "_platform_specific", p, name))
	}
	cands = append(cands, filepath.Join(dir, name))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c
		}
	}
	return ""
}

// installFromPath handles an explicit library file or directory.
func installFromPath(p string, o DiscoverOptions) (*WidevineInstall, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		if in := bestInRoot(p, o); in != nil {
			return in, nil
		}
		return nil, fmt.Errorf("no %s under that directory", WidevineLibraryName(o.GOOS))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	in := &WidevineInstall{Library: abs}
	// the library sits in <ver>/_platform_specific/<plat>/ or directly in <ver>/
	for _, d := range []string{filepath.Dir(abs), filepath.Dir(filepath.Dir(filepath.Dir(abs)))} {
		if m, err := readWidevineManifest(filepath.Join(d, "manifest.json")); err == nil {
			in.Manifest, in.Version, in.Dir = *m, m.Version, d
			break
		}
	}
	return in, nil
}

func readWidevineManifest(path string) (*WidevineManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseWidevineManifest(b)
}

// ParseWidevineManifest parses manifest.json of a Widevine CDM component. The "x-cdm-*" fields are the ones
// Chrome's own component installer reads.
func ParseWidevineManifest(b []byte) (*WidevineManifest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	m := &WidevineManifest{}
	_ = json.Unmarshal(raw["name"], &m.Name)
	_ = json.Unmarshal(raw["version"], &m.Version)
	for _, c := range stringList(raw["x-cdm-codecs"]) {
		m.Codecs = append(m.Codecs, strings.ToLower(c))
	}
	var pl bool
	if json.Unmarshal(raw["x-cdm-persistent-license-support"], &pl) == nil {
		m.PersistentLicense = pl
	}
	for _, s := range stringList(raw["x-cdm-supported-encryption-schemes"]) {
		m.Schemes = append(m.Schemes, Scheme(strings.ToLower(s)))
	}
	m.ModuleVersions = intList(raw["x-cdm-module-versions"])
	m.InterfaceVersions = intList(raw["x-cdm-interface-versions"])
	m.HostVersions = intList(raw["x-cdm-host-versions"])
	return m, nil
}

// stringList reads either "a,b,c" or ["a","b"].
func stringList(r json.RawMessage) []string {
	if len(r) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var l []string
	if json.Unmarshal(r, &l) == nil {
		return l
	}
	return nil
}

// intList reads "4", "10,11", 4 or [10, 11].
func intList(r json.RawMessage) []int {
	var out []int
	for _, s := range stringList(r) {
		if n, err := strconv.Atoi(s); err == nil {
			out = append(out, n)
		}
	}
	if out != nil {
		return out
	}
	var n int
	if json.Unmarshal(r, &n) == nil {
		return []int{n}
	}
	var l []int
	if json.Unmarshal(r, &l) == nil {
		return l
	}
	return nil
}

// compareVersions compares dotted numeric versions ("4.10.2710.0"); non-numeric parts compare as 0.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
