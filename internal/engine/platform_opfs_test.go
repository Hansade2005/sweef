package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// httpTestServer2 is a second origin (another port) serving one static page.
func httpTestServer2(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!doctype html><title>other</title><p>other origin</p>")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestOPFS(t *testing.T) {
	tab := platformTab(t)
	pre := `const root = await navigator.storage.getDirectory();
const names = async (d) => { const o = []; for await (const [n, h] of d.entries()) o.push(n + ':' + h.kind[0]); return o; };
const write = async (dir, name, data) => { const fh = await dir.getFileHandle(name, { create: true }); const w = await fh.createWritable(); await w.write(data); await w.close(); return fh; };
const read = async (fh) => (await fh.getFile()).text();
`
	runJSCases(t, tab, []jsCase{
		{"root handle", pre + `return [root.kind, root.name, root instanceof FileSystemDirectoryHandle, root instanceof FileSystemHandle, String(root), navigator.storage instanceof StorageManager, await root.isSameEntry(await navigator.storage.getDirectory())];`, `["directory","",true,true,"[object FileSystemDirectoryHandle]",true,true]`},
		{"create and read a file", pre + `const fh = await write(root, 'a.txt', 'hello'); const f = await fh.getFile(); return [fh.kind, fh.name, fh instanceof FileSystemFileHandle, f instanceof File, f.name, f.size, f.type, await f.text(), typeof f.lastModified];`, `["file","a.txt",true,true,"a.txt",5,"text/plain","hello","number"]`},
		{"getFileHandle without create fails", pre + `try { await root.getFileHandle('missing.txt'); } catch (e) { return [e.name, e instanceof DOMException]; }`, `["NotFoundError",true]`},
		{"directories nest", pre + `const d = await root.getDirectoryHandle('docs', { create: true }); const sub = await d.getDirectoryHandle('sub', { create: true }); await write(sub, 'n.txt', 'deep'); const again = await (await root.getDirectoryHandle('docs')).getDirectoryHandle('sub'); return [d.kind, d.name, await read(await again.getFileHandle('n.txt')), await sub.isSameEntry(again)];`, `["directory","docs","deep",true]`},
		{"type mismatch", pre + `await root.getDirectoryHandle('tm-dir', { create: true }); await write(root, 'tm-file', 'x'); const out = []; for (const f of [() => root.getFileHandle('tm-dir'), () => root.getDirectoryHandle('tm-file'), () => root.getFileHandle('tm-dir', { create: true })]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["TypeMismatchError","TypeMismatchError","TypeMismatchError"]`},
		{"invalid names", pre + `const out = []; for (const n of ['', '.', '..', 'a/b', 'a\\b']) { try { await root.getFileHandle(n, { create: true }); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["TypeError","TypeError","TypeError","TypeError","TypeError"]`},
		{"entries keys values and sorting", pre + `const d = await root.getDirectoryHandle('list', { create: true }); await write(d, 'b.txt', '2'); await write(d, 'a.txt', '1'); await d.getDirectoryHandle('z', { create: true }); const keys = []; for await (const k of d.keys()) keys.push(k); const vals = []; for await (const v of d.values()) vals.push(v.name + v.kind); const viaIter = []; for await (const [n] of d) viaIter.push(n); return [await names(d), keys, vals, viaIter];`, `[["a.txt:f","b.txt:f","z:d"],["a.txt","b.txt","z"],["a.txtfile","b.txtfile","zdirectory"],["a.txt","b.txt","z"]]`},
		{"removeEntry", pre + `const d = await root.getDirectoryHandle('rm', { create: true }); await write(d, 'f', 'x'); const sub = await d.getDirectoryHandle('s', { create: true }); await write(sub, 'g', 'y'); const out = []; await d.removeEntry('f'); out.push(await names(d)); try { await d.removeEntry('s'); } catch (e) { out.push(e.name); } await d.removeEntry('s', { recursive: true }); out.push(await names(d)); try { await d.removeEntry('s'); } catch (e) { out.push(e.name); } return out;`, `[["s:d"],"InvalidModificationError",[],"NotFoundError"]`},
		{"resolve", pre + `const d = await root.getDirectoryHandle('res', { create: true }); const s = await d.getDirectoryHandle('in', { create: true }); const f = await write(s, 'leaf.txt', 'x'); const other = await write(root, 'elsewhere', 'x'); return [await d.resolve(f), await d.resolve(d), await d.resolve(other), await root.resolve(f)];`, `[["in","leaf.txt"],[],null,["res","in","leaf.txt"]]`},
		{"writable: seek, truncate and positions", pre + `const fh = await root.getFileHandle('w.bin', { create: true }); const w = await fh.createWritable(); await w.write('hello world'); await w.seek(6); await w.write('WORLD'); await w.truncate(8); await w.write({ type: 'write', position: 0, data: 'J' }); await w.write({ type: 'seek', position: 10 }); await w.write('!'); await w.close(); const bytes = new Uint8Array(await (await fh.getFile()).arrayBuffer()); return [Array.from(bytes), new Blob([bytes]).size];`, `[[74,101,108,108,111,32,87,79,0,0,33],11]`},
		{"writable: binary, blob and params", pre + `const fh = await root.getFileHandle('bin', { create: true }); const w = await fh.createWritable(); await w.write(new Uint8Array([1, 2])); await w.write(new Blob(['AB']).slice(0, 1)); await w.write(new Uint16Array([0x0403]).buffer); await w.write(new DataView(new Uint8Array([9]).buffer)); await w.close(); return Array.from(new Uint8Array(await (await fh.getFile()).arrayBuffer()));`, `[1,2,65,3,4,9]`},
		{"writable is atomic until closed", pre + `const fh = await write(root, 'atomic.txt', 'original'); const w = await fh.createWritable(); await w.write('new content'); const mid = await read(fh); await w.close(); return [mid, await read(fh)];`, `["original","new content"]`},
		{"abort discards", pre + `const fh = await write(root, 'abort.txt', 'keep'); const w = await fh.createWritable(); await w.write('lost'); await w.abort(); return await read(fh);`, `"keep"`},
		{"createWritable truncates unless keepExistingData", pre + `const fh = await write(root, 'keep.txt', 'abcdef'); const w = await fh.createWritable({ keepExistingData: true }); await w.write({ type: 'write', position: 2, data: 'XY' }); await w.close(); const kept = await read(fh); const w2 = await fh.createWritable(); await w2.close(); return [kept, (await fh.getFile()).size];`, `["abXYef",0]`},
		{"writable is a WritableStream", pre + `const fh = await root.getFileHandle('ws.txt', { create: true }); const w = await fh.createWritable(); const out = [w instanceof WritableStream, w instanceof FileSystemWritableFileStream, w.locked, String(w)]; const writer = w.getWriter(); await writer.write('via writer'); await writer.close(); out.push(await read(fh)); return out;`, `[true,true,false,"[object FileSystemWritableFileStream]","via writer"]`},
		{"pipeTo a file", pre + `const fh = await root.getFileHandle('piped.txt', { create: true }); await new Response('piped body').body.pipeTo(await fh.createWritable()); return await read(fh);`, `"piped body"`},
		{"write errors", pre + `const fh = await root.getFileHandle('err.txt', { create: true }); const w = await fh.createWritable(); const out = []; for (const f of [() => w.write({ type: 'seek' }), () => w.write({ type: 'truncate' }), () => w.write({ type: 'write' }), () => w.seek(-1)]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["TypeError","TypeError","TypeError","TypeError"]`},
		{"closing after the file was removed", pre + `const fh = await write(root, 'gone.txt', 'x'); const w = await fh.createWritable(); await w.write('y'); await root.removeEntry('gone.txt'); try { await w.close(); } catch (e) { return e.name; }`, `"NotFoundError"`},
		{"getFile after removal", pre + `const fh = await write(root, 'gone2.txt', 'x'); await root.removeEntry('gone2.txt'); try { await fh.getFile(); } catch (e) { return e.name; }`, `"NotFoundError"`},
		{"mime types by extension", pre + `const out = []; for (const n of ['a.json', 'b.png', 'c.unknownext', 'd']) { out.push((await (await write(root, 'm-' + n, 'x')).getFile()).type); } return out;`, `["application/json","image/png","",""]`},
		{"handle move and remove", pre + `const d = await root.getDirectoryHandle('mv', { create: true }); const fh = await write(d, 'old.txt', 'data'); await fh.move('new.txt'); const e1 = await names(d); const sub = await d.getDirectoryHandle('inner', { create: true }); await fh.move(sub, 'moved.txt'); const e2 = [await names(d), await names(sub), fh.name, await read(fh)]; await fh.remove(); return [e1, e2, await names(sub)];`, `[["new.txt:f"],[["inner:d"],["moved.txt:f"],"moved.txt","data"],[]]`},
		{"permissions", pre + `return [await root.queryPermission({ mode: 'readwrite' }), await root.requestPermission({ mode: 'readwrite' })];`, `["granted","granted"]`},
		{"storage manager", pre + `const e = await navigator.storage.estimate(); return [typeof e.usage, e.quota > 0, e.usage > 0, await navigator.storage.persist(), await navigator.storage.persisted()];`, `["number",true,true,false,false]`},
		{"user pickers reject with AbortError", pre + `const out = []; for (const f of [() => showOpenFilePicker(), () => showSaveFilePicker({ suggestedName: 'x.txt' }), () => showDirectoryPicker(), () => showOpenFilePicker({ types: [{ description: 'Images', accept: { 'image/*': ['.png'] } }] })]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["AbortError","AbortError","AbortError","AbortError"]`},
		{"picker option validation", pre + `const out = []; for (const f of [() => showOpenFilePicker(5), () => showOpenFilePicker({ excludeAcceptAllOption: true }), () => showSaveFilePicker({ types: [{ accept: { 'nonsense': ['.x'] } }] })]) { try { await f(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["TypeError","TypeError","TypeError"]`},
		{"handle classes are not constructible", pre + `const out = []; for (const C of [FileSystemHandle, FileSystemFileHandle, FileSystemDirectoryHandle, StorageManager]) { try { new C(); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["TypeError","TypeError","TypeError","TypeError"]`},
		{"binary round trip of all byte values", pre + `const all = new Uint8Array(256).map((_, i) => i); const fh = await write(root, 'all.bin', all); const back = new Uint8Array(await (await fh.getFile()).arrayBuffer()); return [back.length, back.every((v, i) => v === i)];`, `[256,true]`},
	})
}

func TestOPFSPersistsAcrossNavigationAndIsolatesOrigins(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, err := e.NewTab(srv.URL + "/static")
	if err != nil {
		t.Fatal(err)
	}
	mustEval(t, tab, `(async () => { const r = await navigator.storage.getDirectory(); const f = await r.getFileHandle('persist.txt', { create: true }); const w = await f.createWritable(); await w.write('kept'); await w.close(); return 1; })()`)
	if err := tab.Navigate(srv.URL+"/doc", nil); err != nil {
		t.Fatal(err)
	}
	got := mustEval(t, tab, `(async () => { const r = await navigator.storage.getDirectory(); const f = await r.getFileHandle('persist.txt'); return await (await f.getFile()).text(); })()`)
	if got != `"kept"` {
		t.Fatalf("file did not survive a navigation: %s", got)
	}
	other := httpTestServer2(t)
	tab2, err := e.NewTab(other + "/")
	if err != nil {
		t.Fatal(err)
	}
	got = mustEval(t, tab2, `(async () => { const r = await navigator.storage.getDirectory(); try { await r.getFileHandle('persist.txt'); return 'visible'; } catch (e) { return e.name; } })()`)
	if got != `"NotFoundError"` {
		t.Fatalf("another origin must not see the file: %s", got)
	}
}
