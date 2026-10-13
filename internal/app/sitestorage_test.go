package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

// siteDataServer serves a page that writes IndexedDB and a Cache Storage entry, then sets its title to "stored".
func siteDataServer(t testing.TB) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>writing</title></head><body><script>
const q = indexedDB.open('notes', 1);
q.onupgradeneeded = () => q.result.createObjectStore('n');
q.onsuccess = () => {
  const tx = q.result.transaction('n', 'readwrite');
  tx.objectStore('n').put('remember me', 'k');
  tx.oncomplete = () => caches.open('c').then((c) => c.put('/x', new Response('cached'))).then(() => { document.title = 'stored'; });
};
</script></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func countFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// Site data lands in the profile's storage directory and "Clear browsing data" removes it.
func TestSiteStoragePersistsInProfileAndClearBrowsingDataWipesIt(t *testing.T) {
	srv := siteDataServer(t)
	dir := t.TempDir()
	prof, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	prof.Settings.Set("onboarding.done", true)
	h := newHarness(t, hopts{cfg: Config{Profile: prof, InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("stored")
	storage := filepath.Join(dir, "storage")
	if n := countFiles(storage); n < 3 { // meta.json, the database log, an index log and a body
		t.Fatalf("expected site data under %s, found %d files", storage, n)
	}
	h.uiPost("data.clear", nil)
	h.waitFor("site data removed", func() bool { return countFiles(storage) == 0 })
}

// A private window's site data never reaches the disk.
func TestSiteStorageOfPrivateWindowStaysInMemory(t *testing.T) {
	srv := siteDataServer(t)
	dir := t.TempDir()
	prof, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	prof.Settings.Set("onboarding.done", true)
	h := newHarness(t, hopts{cfg: Config{Profile: prof, InitialURLs: []string{"about:blank"}}})
	id := h.app.newTab(srv.URL+"/", true, true)
	h.waitFor("private page stored", func() bool {
		tb, ok := h.app.model.Tab(id)
		return ok && tb.Title == "stored"
	})
	if n := countFiles(filepath.Join(dir, "storage")); n != 0 {
		t.Fatalf("a private tab wrote %d files to the profile's storage", n)
	}
}
