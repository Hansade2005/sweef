// Command zpack writes the compressed embeds of the Sweef binary (see internal/zpack for the formats and why).
//
//	zpack blob [-check] FILE|GLOB...        writes FILE.z, a raw DEFLATE stream, for each source FILE
//	zpack dir  [-check] -o OUT.zar DIR      packs every regular file below DIR into an archive (paths relative to DIR)
//	zpack gofont [-check] -o OUT.zar        packs the eight Go fonts of golang.org/x/image/font/gofont, hinting programs removed (dehint.go)
//	zpack tz   [-check] -o OUT.zar          packs $GOROOT/lib/time/zoneinfo.zip (the IANA database), aliases stored once
//
// With -check nothing is written: the command fails if the existing output does not decode to the current sources
// (it compares decoded content, so a newer Go toolchain's different but equivalent compression is not an error).
// The packages that embed these files run the same check as a unit test, and have a //go:generate line that
// regenerates them.
package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/sweefbrowser/sweef/internal/zpack"
)

// goFonts is the eight Go fonts without their hinting programs (dehint.go).
func goFonts() map[string][]byte {
	out := map[string][]byte{}
	for name, ttf := range goFontsRaw() {
		d, err := dehint(ttf)
		if err != nil {
			fmt.Fprintln(os.Stderr, "zpack:", name, err)
			os.Exit(1)
		}
		out[name] = d
	}
	return out
}

func goFontsRaw() map[string][]byte {
	return map[string][]byte{
		"gobold": gobold.TTF, "gobolditalic": gobolditalic.TTF, "goitalic": goitalic.TTF, "gomono": gomono.TTF,
		"gomonobold": gomonobold.TTF, "gomonobolditalic": gomonobolditalic.TTF, "gomonoitalic": gomonoitalic.TTF,
		"goregular": goregular.TTF,
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fl := flag.NewFlagSet(cmd, flag.ExitOnError)
	check := fl.Bool("check", false, "verify instead of writing")
	out := fl.String("o", "", "output archive")
	fl.Parse(os.Args[2:])
	var err error
	switch cmd {
	case "blob":
		for _, pat := range fl.Args() {
			files, _ := filepath.Glob(pat) // go generate does not expand globs
			if len(files) == 0 {
				err = fmt.Errorf("no files match %q", pat)
				break
			}
			for _, f := range files {
				if err = blob(f, *check); err != nil {
					break
				}
			}
		}
	case "dir":
		if fl.NArg() != 1 || *out == "" {
			usage()
		}
		var files map[string][]byte
		if files, err = readDir(fl.Arg(0)); err == nil {
			err = archive(*out, files, *check)
		}
	case "gofont":
		if *out == "" {
			usage()
		}
		err = archive(*out, goFonts(), *check)
	case "tz":
		if *out == "" {
			usage()
		}
		var files map[string][]byte
		if files, err = readZoneinfo(); err == nil {
			err = archive(*out, files, *check)
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zpack:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: zpack blob [-check] FILE... | zpack dir [-check] -o OUT DIR | zpack gofont [-check] -o OUT | zpack tz [-check] -o OUT")
	os.Exit(2)
}

func blob(src string, check bool) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dst := src + ".z"
	if check {
		z, err := os.ReadFile(dst)
		if err != nil {
			return fmt.Errorf("%s: %w (run go generate)", dst, err)
		}
		if got := zpack.Inflate(string(z)); !bytes.Equal(got, b) {
			return fmt.Errorf("%s is stale: it does not decode to %s (run go generate)", dst, src)
		}
		return nil
	}
	return writeIfChanged(dst, zpack.Deflate(b), func(old []byte) bool { return bytes.Equal(zpack.Inflate(string(old)), b) })
}

func archive(dst string, files map[string][]byte, check bool) error {
	if check {
		z, err := os.ReadFile(dst)
		if err != nil {
			return fmt.Errorf("%s: %w (run go generate)", dst, err)
		}
		if !archiveMatches(z, files) {
			return fmt.Errorf("%s is stale (run go generate)", dst)
		}
		return nil
	}
	return writeIfChanged(dst, zpack.Build(files), func(old []byte) bool { return archiveMatches(old, files) })
}

func archiveMatches(z []byte, files map[string][]byte) bool {
	a, err := zpack.Open(string(z))
	if err != nil || len(a.Names()) != len(files) {
		return false
	}
	for n, want := range files {
		got, ok := a.Read(n)
		if !ok || !bytes.Equal(got, want) {
			return false
		}
	}
	return true
}

// writeIfChanged keeps the old file when it already decodes to the same content, so regenerating with a newer Go
// toolchain does not churn the repository.
func writeIfChanged(dst string, data []byte, same func(old []byte) bool) error {
	if old, err := os.ReadFile(dst); err == nil && same(old) {
		return nil
	}
	return os.WriteFile(dst, data, 0o644)
}

func readDir(root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err
}

func readZoneinfo() (map[string][]byte, error) {
	goroot := runtime.GOROOT()
	zr, err := zip.OpenReader(filepath.Join(goroot, "lib", "time", "zoneinfo.zip"))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = b
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 || strings.HasPrefix(names[0], "/") {
		return nil, fmt.Errorf("unexpected zoneinfo.zip layout")
	}
	return files, nil
}
