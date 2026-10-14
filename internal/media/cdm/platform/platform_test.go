package platform

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

func TestRegistryReportsEveryKeySystemWithAReason(t *testing.T) {
	t.Setenv(cdm.WidevineEnv, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	r := NewRegistry(Options{})
	want := []string{
		"com.apple.fps", "com.microsoft.playready", "com.microsoft.playready.recommendation", "com.widevine.alpha", "org.w3.clearkey",
	}
	var got []string
	for _, st := range r.Report() {
		got = append(got, st.KeySystem)
		if st.KeySystem == cdm.ClearKeySystem {
			if !st.Availability.Supported {
				t.Error("Clear Key must always be available")
			}
			continue
		}
		if st.Availability.Supported {
			t.Errorf("%s reports supported on a machine with no CDM, and with the unverified backends off", st.KeySystem)
		}
		if st.Availability.Reason == "" {
			t.Errorf("%s is unavailable without saying why", st.KeySystem)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("registered %v, want %v", got, want)
	}
	if p, ok := r.Lookup("com.apple.fps.1_0"); !ok || p.KeySystem() != "com.apple.fps" {
		t.Fatal("legacy FairPlay alias")
	}
	if ks := r.KeySystems(); len(ks) != 1 || ks[0] != cdm.ClearKeySystem {
		t.Fatalf("available key systems %v", ks)
	}
}

// The static Linux browser binary may import the key system layer only if none of it pulls in dlopen: purego (and
// with it fakecgo's dynamic loader import) must stay confined to the helper binary.
func TestKeySystemLayerStaysFreeOfDlopen(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool")
	}
	for _, goos := range []string{"linux", "freebsd"} {
		cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".", "github.com/sweefbrowser/sweef/internal/media/cdm")
		cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0", "GOOS="+goos)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, imp := range strings.Fields(string(out)) {
			if strings.Contains(imp, "ebitengine/purego") || strings.HasSuffix(imp, "/cdm/widevinehost") {
				t.Errorf("GOOS=%s: the browser-side key system layer imports %s", goos, imp)
			}
		}
	}
}
