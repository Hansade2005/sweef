package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// navigator.storage.persist() asks the application's policy; without one, and in memory-only contexts, it
// answers no.
func TestStoragePersistFollowsThePolicy(t *testing.T) {
	srv := storageServer(t)
	dir := t.TempDir()
	e := newStorageEngine(t, dir)
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `return JSON.stringify([await navigator.storage.persisted(), await navigator.storage.persist(), await navigator.storage.persisted()]);`)
	expect(t, tab, got, `[false,false,false]`)

	var asked []string
	e.DefaultContext().SiteStorage().SetPersistPolicy(func(origin string) bool { asked = append(asked, origin); return true })
	got = js(t, tab, `return JSON.stringify([await navigator.storage.persist(), await navigator.storage.persisted()]);`)
	expect(t, tab, got, `[true,true]`)
	if len(asked) != 1 || asked[0] != siteOriginOf(srv.URL) {
		t.Fatalf("the policy was asked %v", asked)
	}

	// a private context has no disk: persistence is meaningless there, whatever the policy says
	priv := e.NewContext("private", ContextOptions{})
	priv.SiteStorage().SetPersistPolicy(func(string) bool { return true })
	ptab, err := priv.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got = js(t, ptab, `return JSON.stringify([await navigator.storage.persist(), await navigator.storage.persisted()]);`)
	expect(t, ptab, got, `[false,false]`)
}

// The origin private file system is on disk, counts against the origin's quota and reports in estimate().
func TestOPFSIsOnDiskAndUnderQuota(t *testing.T) {
	srv := storageServer(t)
	dir := t.TempDir()
	mk := func() *Engine {
		o := DefaultOptions()
		o.StorageDir = dir
		o.StorageQuotaMiB = 1
		return New(o)
	}
	e1 := mk()
	tab := openTab(t, e1, srv.URL+"/")
	got := js(t, tab, `
const root = await navigator.storage.getDirectory();
const write = async (name, data) => { const fh = await root.getFileHandle(name, { create: true }); const w = await fh.createWritable(); await w.write(data); await w.close(); };
await write('a.bin', new Uint8Array(600 * 1024).fill(7));
let second;
try { await write('b.bin', new Uint8Array(600 * 1024).fill(8)); second = 'ok'; } catch (e) { second = e.name; }
const est = await navigator.storage.estimate();
return JSON.stringify({ second, fs: est.usageDetails.fileSystem > 600 * 1024, total: est.usage >= est.usageDetails.fileSystem, quota: est.quota });
`)
	expect(t, tab, got, `{"second":"QuotaExceededError","fs":true,"total":true,"quota":1048576}`)
	e1.Close()
	if blobs, _ := filepath.Glob(filepath.Join(dir, "*", "blobs", "*")); len(blobs) != 1 {
		t.Fatalf("the large file must be a file on disk: %v", blobs)
	}

	// it survives a restart (the tree used to be memory-only)
	e2 := mk()
	defer e2.Close()
	tab2 := openTab(t, e2, srv.URL+"/")
	got = js(t, tab2, `
const root = await navigator.storage.getDirectory();
const f = await (await root.getFileHandle('a.bin')).getFile();
const names = []; for await (const [n] of root) names.push(n);
const b = new Uint8Array(await f.arrayBuffer());
return JSON.stringify({ names, size: f.size, ok: b[0] === 7 && b[b.length - 1] === 7 });
`)
	expect(t, tab2, got, `{"names":["a.bin","b.bin"],"size":614400,"ok":true}`) // b.bin was created (empty) before its write was refused

	// a private context keeps it in memory only
	priv := e2.NewContext("private", ContextOptions{})
	ptab, err := priv.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := filepath.Glob(filepath.Join(dir, "*", "blobs", "*"))
	got = js(t, ptab, `
const root = await navigator.storage.getDirectory();
const fh = await root.getFileHandle('p.bin', { create: true }); const w = await fh.createWritable(); await w.write(new Uint8Array(200 * 1024)); await w.close();
return String((await (await root.getFileHandle('p.bin')).getFile()).size);
`)
	expect(t, ptab, got, `204800`)
	after, _ := filepath.Glob(filepath.Join(dir, "*", "blobs", "*"))
	if len(after) != len(before) {
		t.Fatalf("a private context wrote files: %v -> %v", before, after)
	}
}

// Under global pressure a whole least-recently-used origin is deleted: its open database gets the close
// event, its requests fail, and the persistent origin is spared.
func TestEvictionClosesDatabasesOfTheEvictedOrigin(t *testing.T) {
	a, b, keep := storageServer(t), storageServer(t), storageServer(t)
	dir := t.TempDir()
	o := DefaultOptions()
	o.StorageDir = dir
	o.StorageBudgetMiB = 4
	o.StorageQuotaMiB = 3
	e := New(o)
	defer e.Close()
	write := func(tab *Tab, name string, n int) string {
		return js(t, tab, idbHelpers+`
const db = await open('`+name+`', 1, (db) => db.createObjectStore('s'));
window.__db = db; window.__closed = false;
db.onclose = () => { window.__closed = true; };
const tx = db.transaction('s', 'readwrite');
for (let i = 0; i < `+itoa(n)+`; i++) tx.objectStore('s').put('x'.repeat(100 * 1024), i);
return await done(tx);
`)
	}
	tabKeep := openTab(t, e, keep.URL+"/")
	expect(t, tabKeep, write(tabKeep, "keep", 12), `complete`) // about 1.2 MiB, will be persistent
	e.DefaultContext().SiteStorage().SetPersistPolicy(func(origin string) bool { return origin == siteOriginOf(keep.URL) })
	if got := js(t, tabKeep, `return String(await navigator.storage.persist());`); got != "true" {
		t.Fatalf("persist: %s", got)
	}
	tabA := openTab(t, e, a.URL+"/")
	expect(t, tabA, write(tabA, "a", 15), `complete`) // 1.5 MiB: total 2.7 of 4
	time.Sleep(20 * time.Millisecond)
	tabB := openTab(t, e, b.URL+"/")
	expect(t, tabB, write(tabB, "b", 20), `complete`) // 2 MiB more: 4.7 > 4, the least recently used best-effort origin (a) must go

	st := e.DefaultContext().SiteStorage()
	if u := st.Origin(siteOriginOf(a.URL)).Usage().Total; u != 0 {
		t.Fatalf("the evicted origin still holds %d bytes", u)
	}
	if u := st.Origin(siteOriginOf(keep.URL)).Usage().Total; u < 1<<20 {
		t.Fatalf("the persistent origin lost data: %d", u)
	}
	// the evicted page's connection was closed by the browser
	var closed string
	for i := 0; i < 50; i++ {
		closed = js(t, tabA, `return String(window.__closed);`)
		if closed == "true" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if closed != "true" {
		t.Fatalf("IDBDatabase.onclose did not fire in the evicted origin's page\nconsole:\n%s", consoleText(tabA))
	}
	got := js(t, tabA, `
let err = '';
try { window.__db.transaction('s'); } catch (e) { err = e.name; }
return JSON.stringify({ err, dbs: await indexedDB.databases() });
`)
	expect(t, tabA, got, `{"err":"InvalidStateError","dbs":[]}`)
	// the evicted origin can start again
	expect(t, tabA, write(tabA, "a2", 3), `complete`)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
