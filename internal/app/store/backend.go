// Package store holds every persistent (or deliberately non-persistent)
// piece of browser data: history, bookmarks, downloads, settings, session,
// site permissions, top sites and usage stats.
//
// Storage format: one append-friendly JSONL log per collection (a torn last
// line after a crash is skipped at load) plus atomic whole-file snapshots
// (write temp, fsync, rename) for small documents. Everything is pure Go,
// loaded into memory at start-up and indexed there. An incognito profile
// uses MemBackend and never touches the file system.
package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Backend is the tiny file-system surface the stores need.
type Backend interface {
	// Append adds one record (a line without trailing newline) to a log.
	Append(name string, rec []byte) error
	// ReadFile returns the whole file or an error wrapping os.ErrNotExist.
	ReadFile(name string) ([]byte, error)
	// WriteAtomic replaces a file so that readers see the old or the new
	// content, never a mix, even if the process dies mid-write.
	WriteAtomic(name string, data []byte) error
	Remove(name string) error
	// Persistent reports whether data survives the process.
	Persistent() bool
	Close() error
}

// ---- disk -----------------------------------------------------------------

// DiskBackend stores files under Dir with 0600 permissions (0700 for Dir).
type DiskBackend struct {
	Dir string
	mu  sync.Mutex
	fh  map[string]*os.File
}

// NewDiskBackend creates dir if needed.
func NewDiskBackend(dir string) (*DiskBackend, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &DiskBackend{Dir: dir, fh: map[string]*os.File{}}, nil
}

func (d *DiskBackend) path(name string) string { return filepath.Join(d.Dir, filepath.Base(name)) }

func (d *DiskBackend) Append(name string, rec []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.fh[name]
	if f == nil {
		var err error
		f, err = os.OpenFile(d.path(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		d.fh[name] = f
	}
	buf := make([]byte, 0, len(rec)+1)
	buf = append(append(buf, rec...), '\n')
	_, err := f.Write(buf) // single write: a crash tears at most the last record
	return err
}

func (d *DiskBackend) ReadFile(name string) ([]byte, error) { return os.ReadFile(d.path(name)) }

func (d *DiskBackend) WriteAtomic(name string, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.fh[name]; f != nil { // an open append handle would point at the old inode
		f.Close()
		delete(d.fh, name)
	}
	tmp := d.path(name) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if runtime.GOOS == "windows" { // rename-over fails on some Windows setups
		_ = os.Remove(d.path(name))
	}
	return os.Rename(tmp, d.path(name))
}

func (d *DiskBackend) Remove(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.fh[name]; f != nil {
		f.Close()
		delete(d.fh, name)
	}
	err := os.Remove(d.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (d *DiskBackend) Persistent() bool { return true }

func (d *DiskBackend) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for n, f := range d.fh {
		f.Close()
		delete(d.fh, n)
	}
	return nil
}

// ---- memory ---------------------------------------------------------------

// MemBackend keeps everything in RAM. It is the incognito backend.
type MemBackend struct {
	mu    sync.Mutex
	files map[string][]byte
}

func NewMemBackend() *MemBackend { return &MemBackend{files: map[string][]byte{}} }

func (m *MemBackend) Append(name string, rec []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = append(append(m.files[name], rec...), '\n')
	return nil
}

func (m *MemBackend) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return bytes.Clone(b), nil
}

func (m *MemBackend) WriteAtomic(name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = bytes.Clone(data)
	return nil
}

func (m *MemBackend) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
	return nil
}

func (m *MemBackend) Persistent() bool { return false }

// Wipe forgets everything (incognito window closed).
func (m *MemBackend) Wipe() {
	m.mu.Lock()
	m.files = map[string][]byte{}
	m.mu.Unlock()
}

func (m *MemBackend) Close() error { m.Wipe(); return nil }

// ---- helpers --------------------------------------------------------------

// forEachLine calls fn for each complete, non-empty line of data. A final
// line without a newline is a torn write and is skipped.
func forEachLine(data []byte, fn func(line []byte)) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return // torn tail
		}
		if i > 0 {
			fn(data[:i])
		}
		data = data[i+1:]
	}
}
