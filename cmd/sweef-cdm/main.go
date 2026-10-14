// Command sweef-cdm is the CDM host helper: a separate process that loads a vendor Content Decryption Module (the
// Widevine shared library the user already has) and serves the framed CDM host protocol of internal/media/cdm on
// its standard input and output.
//
// It is a separate binary because loading a shared library needs the dynamic loader, and the Sweef browser binary
// on Linux is fully static. The helper is the one place in the project that is allowed to be dynamically linked.
// Sweef starts it on demand (internal/media/cdm.FindHelper); it exits when its standard input closes.
//
//	sweef-cdm -cdm /path/to/libwidevinecdm.so          serve the protocol (what Sweef runs)
//	sweef-cdm -probe -cdm /path/to/libwidevinecdm.so   load the library, print what it reports, exit
//
// For Verified Media Path the signature of this executable is read from <this executable>.sig (the file
// castLabs EVS writes). See docs/drm.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
	"github.com/sweefbrowser/sweef/internal/media/cdm/widevinehost"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("sweef-cdm", flag.ContinueOnError)
	lib := fs.String("cdm", os.Getenv(cdm.WidevineEnv), "path of the CDM shared library")
	ks := fs.String("key-system", cdm.WidevineKeySystem, "key system passed to CreateCdmInstance")
	ifaces := fs.String("interface-versions", "", "comma separated CDM interface versions to try (10, 11, 12), most preferred first")
	probe := fs.Bool("probe", false, "load the CDM, print its version and exit")
	verbose := fs.Bool("v", false, "log to stderr")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if *lib == "" {
		fmt.Fprintln(os.Stderr, "sweef-cdm: no CDM library: pass -cdm or set", cdm.WidevineEnv)
		return 2
	}
	logf := func(string, ...any) {}
	if *verbose || *probe {
		logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "sweef-cdm: "+f+"\n", a...) }
	}

	// The protocol owns stdout; anything the CDM prints must not corrupt it.
	in, out, err := protocolStreams()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sweef-cdm:", err)
		return 1
	}

	eng, err := widevinehost.Open(widevinehost.Config{
		Library:           *lib,
		KeySystem:         *ks,
		InterfaceVersions: parseVersions(*ifaces),
		Logf:              logf,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweef-cdm: %s/%s: %v\n", runtime.GOOS, runtime.GOARCH, err)
		return 1
	}
	if *probe {
		info := eng.Info()
		fmt.Fprintf(os.Stderr, "key system %s, CDM version %q, interface version %d, VMP signature present: %v\n",
			info.KeySystem, info.CDMVersion, info.InterfaceVersion, info.VMPSigned)
		_ = eng.Close()
		return 0
	}
	err = cdm.Serve(context.Background(), struct {
		io.Reader
		io.Writer
	}{in, out}, eng)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sweef-cdm:", err)
		return 1
	}
	return 0
}

func parseVersions(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}
