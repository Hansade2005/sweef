package app

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

// Persistent storage is granted for bookmarked sites and, through the prompt the embedder installs, for sites
// the person agrees to; everyone else is refused and can be evicted.
func TestStoragePersistPolicyFollowsBookmarksAndThePrompt(t *testing.T) {
	prof, err := store.OpenProfile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof.Settings.Set("onboarding.done", true)
	h := newHarness(t, hopts{cfg: Config{Profile: prof, InitialURLs: []string{"about:blank"}}})
	a := h.app
	const site = "https://shop.example"
	if a.storagePersistPolicy(site) {
		t.Fatal("a site nobody vouched for must be refused")
	}
	var asked []string
	a.SetStoragePersistPrompt(func(origin string) bool { asked = append(asked, origin); return origin == "https://chat.example" })
	if a.storagePersistPolicy(site) || !a.storagePersistPolicy("https://chat.example") || len(asked) != 2 {
		t.Fatalf("the prompt decides for sites without a bookmark: asked %v", asked)
	}
	a.SetStoragePersistPrompt(nil)
	if _, err := prof.Bookmarks.Toggle("Shop", site+"/cart?x=1"); err != nil {
		t.Fatal(err)
	}
	if !a.storagePersistPolicy(site) {
		t.Fatal("a bookmarked site is granted persistent storage")
	}
	if a.storagePersistPolicy("https://other.example") {
		t.Fatal("a bookmark vouches for its own origin only")
	}
	// the engine asks the same policy: navigator.storage.persist() on the default context
	if !a.eng.DefaultContext().SiteStorage().Origin(site).RequestPersist() {
		t.Fatal("the engine does not consult the application's policy")
	}
	if a.eng.DefaultContext().SiteStorage().Origin("https://other.example").RequestPersist() {
		t.Fatal("the engine granted a site the policy refused")
	}
}

// Settings shows what each site stores and clears one site.
func TestSiteStorageIsListedAndCanBeClearedPerSite(t *testing.T) {
	srv := siteDataServer(t)
	prof, err := store.OpenProfile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof.Settings.Set("onboarding.done", true)
	h := newHarness(t, hopts{cfg: Config{Profile: prof, InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("stored")
	origin := store.OriginOf(srv.URL)
	st := h.app.storageState(false)
	sites, _ := st["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("storage state: %+v", st)
	}
	row := sites[0].(map[string]any)
	if row["origin"] != origin || row["bytes"].(int64) <= 0 || row["persisted"] != false {
		t.Fatalf("row: %+v", row)
	}
	if st["budget"].(int64) <= 0 {
		t.Fatalf("budget: %+v", st)
	}
	if priv := h.app.storageState(true); len(priv["sites"].([]any)) != 0 {
		t.Fatal("a private window must not list the sites of normal ones")
	}
	h.uiPost("data.storageClear", map[string]any{"origin": origin})
	h.waitFor("site cleared", func() bool { return len(h.app.storageState(false)["sites"].([]any)) == 0 })
}
