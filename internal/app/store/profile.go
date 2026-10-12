package store

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultDir returns the per-OS profile directory:
// Linux $XDG_DATA_HOME/sweef (or ~/.local/share/sweef), Windows
// %APPDATA%\Sweef, macOS ~/Library/Application Support/Sweef.
func DefaultDir() string { return defaultDirFor(runtime.GOOS, os.Getenv, os.UserHomeDir) }

func defaultDirFor(goos string, getenv func(string) string, home func() (string, error)) string {
	switch goos {
	case "windows":
		if a := getenv("APPDATA"); a != "" {
			return filepath.Join(a, "Sweef")
		}
		h, _ := home()
		return filepath.Join(h, "AppData", "Roaming", "Sweef")
	case "darwin":
		h, _ := home()
		return filepath.Join(h, "Library", "Application Support", "Sweef")
	}
	if x := getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "sweef")
	}
	h, _ := home()
	return filepath.Join(h, ".local", "share", "sweef")
}

// Profile bundles every store over one Backend.
type Profile struct {
	Dir       string
	Incognito bool
	B         Backend

	History   *History
	Bookmarks *Bookmarks
	Downloads *Downloads
	Settings  *Settings
	Session   *SessionStore
	Sites     *Sites
	Stats     *Usage
}

// OpenProfile opens (creating) the persistent profile in dir.
func OpenProfile(dir string) (*Profile, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	b, err := NewDiskBackend(dir)
	if err != nil {
		return nil, err
	}
	return openOn(b, dir, false)
}

// OpenIncognito returns a profile that lives only in memory. Nothing is
// created on disk; Close wipes it.
func OpenIncognito() *Profile {
	p, _ := openOn(NewMemBackend(), "", true)
	return p
}

func openOn(b Backend, dir string, incog bool) (*Profile, error) {
	p := &Profile{Dir: dir, Incognito: incog, B: b}
	var err error
	if p.Settings, err = OpenSettings(b); err != nil {
		return nil, err
	}
	p.History = OpenHistory(b)
	p.Bookmarks = OpenBookmarks(b)
	p.Downloads = OpenDownloads(b)
	p.Session = OpenSession(b)
	p.Sites = OpenSites(b)
	p.Stats = NewUsage()
	return p, nil
}

// Close flushes and releases the profile; an incognito profile is wiped.
func (p *Profile) Close() error {
	p.Session.Flush()
	return p.B.Close()
}
