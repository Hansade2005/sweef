package zpack

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"sort"
	"sync"
	"time"
)

// FS serves the entries of an archive as a read-only file system, inflating a file when it is opened. Files
// implement io.Seeker and io.ReaderAt, so net/http.FS can serve them. It behaves like embed.FS: no modification
// times, regular files 0444, directories 0555.
type FS struct {
	Z string // the archive, as embedded

	once sync.Once
	a    *Archive
	err  error
	dirs map[string][]string // directory path ("." for the root) -> sorted child names
}

func (f *FS) init() error {
	f.once.Do(func() {
		f.a, f.err = Open(f.Z)
		if f.err != nil {
			return
		}
		f.dirs = map[string][]string{".": nil}
		seen := map[string]bool{".": true}
		for _, n := range f.a.Names() {
			for child := n; !seen[child]; {
				seen[child] = true
				dir := path.Dir(child)
				f.dirs[dir] = append(f.dirs[dir], path.Base(child))
				if dir == "." {
					break
				}
				child = dir
			}
		}
		for d := range f.dirs {
			sort.Strings(f.dirs[d])
		}
	})
	return f.err
}

// Open implements fs.FS.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if err := f.init(); err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if kids, ok := f.dirs[name]; ok {
		return &dir{fs: f, name: name, kids: kids}, nil
	}
	b, ok := f.a.Read(name)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &file{Reader: bytes.NewReader(b), name: name, size: int64(len(b))}, nil
}

// ReadFile implements fs.ReadFileFS without the extra copy Open+ReadAll would make.
func (f *FS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	if err := f.init(); err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	if b, ok := f.a.Read(name); ok {
		return b, nil
	}
	if _, ok := f.dirs[name]; ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
}

// ReadDir implements fs.ReadDirFS.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	d, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	dd, ok := d.(*dir)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return dd.ReadDir(-1)
}

type info struct {
	name string
	size int64
	dir  bool
}

func (i info) Name() string { return i.name }
func (i info) Size() int64  { return i.size }
func (i info) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i info) ModTime() time.Time         { return time.Time{} }
func (i info) IsDir() bool                { return i.dir }
func (i info) Sys() any                   { return nil }
func (i info) Type() fs.FileMode          { return i.Mode().Type() }
func (i info) Info() (fs.FileInfo, error) { return i, nil }

type file struct {
	*bytes.Reader
	name string
	size int64
}

func (f *file) Stat() (fs.FileInfo, error) { return info{path.Base(f.name), f.size, false}, nil }
func (f *file) Close() error               { return nil }

type dir struct {
	fs   *FS
	name string
	kids []string
	pos  int
}

func (d *dir) Stat() (fs.FileInfo, error) { return info{path.Base(d.name), 0, true}, nil }
func (d *dir) Close() error               { return nil }
func (d *dir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: fs.ErrInvalid}
}

func (d *dir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.kids[d.pos:]
	if n > 0 && len(rest) == 0 {
		return nil, io.EOF
	}
	if n > 0 && len(rest) > n {
		rest = rest[:n]
	}
	d.pos += len(rest)
	out := make([]fs.DirEntry, 0, len(rest))
	for _, k := range rest {
		p := path.Join(d.name, k)
		if _, isDir := d.fs.dirs[p]; isDir {
			out = append(out, info{k, 0, true})
		} else {
			sz, _ := d.fs.a.Size(p)
			out = append(out, info{k, int64(sz), false})
		}
	}
	return out, nil
}
