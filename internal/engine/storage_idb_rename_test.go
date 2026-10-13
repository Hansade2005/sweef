package engine

import "testing"

func TestIDBRenameStoreAndIndexPersist(t *testing.T) {
	srv := storageServer(t)
	dir := t.TempDir()
	mk := func() *Engine { o := DefaultOptions(); o.StorageDir = dir; return New(o) }
	e1 := mk()
	tab := openTab(t, e1, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('ren', 1, (db) => {
  const s = db.createObjectStore('old', { keyPath: 'id' });
  s.createIndex('byA', 'a');
  s.put({ id: 1, a: 'x' });
});
const upgraded = await new Promise((res) => {
  db.close();
  const q = indexedDB.open('ren', 2);
  q.onupgradeneeded = () => {
    const s = q.transaction.objectStore('old');
    s.name = 'renamed';
    s.index('byA').name = 'byLetter';
    q.result.createObjectStore('other').name = 'other2';
  };
  q.onsuccess = () => res(q.result);
});
const names = [...upgraded.objectStoreNames].join();
const tx = upgraded.transaction('renamed');
const hit = await req(tx.objectStore('renamed').index('byLetter').get('x'));
upgraded.close();
return JSON.stringify({ names, hit, idx: [...tx.objectStore('renamed').indexNames] });
`)
	expect(t, tab, got, `{"names":"other2,renamed","hit":{"id":1,"a":"x"},"idx":["byLetter"]}`)
	e1.Close()
	e2 := mk()
	defer e2.Close()
	tab2 := openTab(t, e2, srv.URL+"/")
	got = js(t, tab2, idbHelpers+`
const db = await open('ren');
const s = db.transaction('renamed').objectStore('renamed');
return JSON.stringify({ names: [...db.objectStoreNames].join(), idx: [...s.indexNames], v: await req(s.index('byLetter').getKey('x')) });
`)
	expect(t, tab2, got, `{"names":"other2,renamed","idx":["byLetter"],"v":1}`)
}
