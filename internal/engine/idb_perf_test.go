package engine

import (
	"encoding/json"
	"testing"
	"time"
)

// A large store reads in bounded time: getAll/getAllKeys/getAll(range, count)/index getAll are single host calls,
// and cursors that are only stepped (continue()) read ahead instead of seeking once per step. Writes made while a
// cursor runs are seen by it (the read-ahead is dropped), key-only cursors and index cursors agree with each other.
func TestIDBLargeStoreAndCursorReadAhead(t *testing.T) {
	tab := platformTab(t)
	t0 := time.Now()
	r, err := tab.Evaluate(`(async function(){
	  const N = 2000, out = {};
	  const db = await new Promise((res, rej) => { const q = indexedDB.open('perf' + Date.now(), 1); q.onupgradeneeded = () => { const s = q.result.createObjectStore('s', { keyPath: 'id' }); s.createIndex('byG', 'g'); }; q.onsuccess = () => res(q.result); q.onerror = () => rej(q.error); });
	  await new Promise((res, rej) => { const tx = db.transaction('s', 'readwrite'); const s = tx.objectStore('s'); for (let i = 0; i < N; i++) s.put({ id: i, g: i % 10, name: 'item' + i }); tx.oncomplete = res; tx.onerror = () => rej(tx.error); });
	  const req = (o) => new Promise((res, rej) => { o.onsuccess = () => res(o.result); o.onerror = () => rej(o.error); });
	  const s = (m) => db.transaction('s', m || 'readonly').objectStore('s');
	  out.getAll = (await req(s().getAll())).length;
	  out.getAllKeys = (await req(s().getAllKeys())).length;
	  out.range = (await req(s().getAll(IDBKeyRange.bound(100, 199)))).length;
	  out.count = (await req(s().getAll(null, 50))).length;
	  out.idx = (await req(s().index('byG').getAll(3))).length;
	  const lim = (p, what) => Promise.race([p, new Promise((r) => setTimeout(() => r('TIMEOUT ' + what), 2500))]);
	  const walk = (store, open, check) => new Promise((res, rej) => { let n = 0, last = -1, ok = true; const c = open(store); c.onsuccess = () => { const cur = c.result; if (cur) { n++; if (check) ok = check(cur, n) && ok; cur.continue(); } else res([n, ok]); }; c.onerror = () => rej(c.error); });
	  let t = performance.now();
	  out.cursor = await lim(walk(s(), (st) => st.openCursor(), (cur, n) => cur.primaryKey === n - 1 && cur.value.name === 'item' + (n - 1)), 'cursor');
	  out.cursorMs = Math.round(performance.now() - t); t = performance.now();
	  out.keyCursor = await lim(walk(s(), (st) => st.openKeyCursor(), (cur, n) => cur.key === n - 1 && cur.value === undefined), 'keyCursor');
	  out.keyCursorMs = Math.round(performance.now() - t);
	  out.prev = await lim(walk(s(), (st) => st.openCursor(null, 'prev'), (cur, n) => cur.key === N - n), 'prev');
	  out.range2 = await lim(walk(s(), (st) => st.openCursor(IDBKeyRange.bound(10, 20, true, false)), (cur, n) => cur.key === 10 + n), 'range2');
	  out.idxCursor = await lim(walk(s(), (st) => st.index('byG').openCursor(IDBKeyRange.only(7)), (cur, n) => cur.key === 7 && cur.value.g === 7), 'idxCursor');
	  out.unique = await lim(walk(s(), (st) => st.index('byG').openKeyCursor(null, 'nextunique'), (cur, n) => cur.key === n - 1), 'unique');
	  // continue(key) mixes with read-ahead
	  out.skip = await lim(new Promise((res) => { const seen = []; const c = s().openCursor(); c.onsuccess = () => { const cur = c.result; if (!cur) return res(seen.join()); seen.push(cur.key); if (seen.length === 3) cur.continue(100); else if (seen.length < 6) cur.continue(); else res(seen.join()); }; }), 'skip');
	  // writes made during a cursor run are seen: delete the next row, add one past the end, update the current one
	  out.writes = await lim(new Promise((res, rej) => {
	    const tx = db.transaction('s', 'readwrite'), st = tx.objectStore('s'), seen = [];
	    const c = st.openCursor(IDBKeyRange.bound(1000, 1040));
	    let step = 0;
	    c.onsuccess = () => { const cur = c.result; if (!cur) { return; } seen.push(cur.key); step++;
	      if (step === 3) { st.delete(cur.key + 1); st.put({ id: 1000.5, g: 0, name: 'inserted' }); }
	      if (step === 4) { cur.update({ id: cur.key, g: 99, name: 'changed' }); }
	      cur.continue(); };
	    tx.oncomplete = async () => { res(seen.slice(0, 12).join() + '|' + String(await req(s().get(1003))) + ',' + (await req(s().get(1004))).name); };
	    tx.onerror = () => rej(tx.error);
	  }), 'writes');
	  return JSON.stringify(out);
	})()`)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	_ = json.Unmarshal(r, &s)
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	t.Logf("%s (in %v)", s, time.Since(t0))
	eq := func(k string, want any) {
		t.Helper()
		got, _ := json.Marshal(out[k])
		w, _ := json.Marshal(want)
		if string(got) != string(w) {
			t.Errorf("%s = %s, want %s", k, got, w)
		}
	}
	eq("getAll", 2000.0)
	eq("getAllKeys", 2000.0)
	eq("range", 100.0)
	eq("count", 50.0)
	eq("idx", 200.0)
	eq("cursor", []any{2000.0, true})
	eq("keyCursor", []any{2000.0, true})
	eq("prev", []any{2000.0, true})
	eq("range2", []any{10.0, true})
	eq("idxCursor", []any{200.0, true})
	eq("unique", []any{10.0, true})
	eq("skip", "0,1,2,100,101,102")
	// 1000,1001,1002 seen, then 1003 deleted and 1000.5 inserted behind the cursor (not seen), 1004 changed in place
	eq("writes", "1000,1001,1002,1004,1005,1006,1007,1008,1009,1010,1011,1012|undefined,changed")
	if ms, _ := out["cursorMs"].(float64); ms > 3000 {
		t.Errorf("walking 2000 rows with a cursor took %v ms", ms)
	}
}
