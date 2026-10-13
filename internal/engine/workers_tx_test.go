package engine

import "testing"

// Overlapping readwrite transactions of a page and a worker queue instead of interleaving: each one sees the
// other's committed result.
func TestWorkerAndPageReadWriteTransactionsSerialise(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/rpc.js": rpcWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, wkIDBHelpers+pageRPC+`
const db = await open('ser', 1, (d) => { d.createObjectStore('kv'); });
let tx = db.transaction('kv', 'readwrite'); tx.objectStore('kv').put('', 'log'); await done(tx);
const w = new Worker('/rpc.js');
await rpc(w, "self.db = await open('ser', 1); return 1;");
// the worker's transaction body: append 'W' five times in one readwrite transaction
const workerRun = rpc(w, "const tx = self.db.transaction('kv', 'readwrite'); const s = tx.objectStore('kv'); let n = 0; const step = () => { const g = s.get('log'); g.onsuccess = () => { s.put(g.result + 'W', 'log'); if (++n < 5) step(); }; }; step(); await done(tx); return 1;");
// the page appends 'P' twenty times in one readwrite transaction that starts at the same moment
const tx2 = db.transaction('kv', 'readwrite'); const s2 = tx2.objectStore('kv'); let n = 0;
const step = () => { const g = s2.get('log'); g.onsuccess = () => { s2.put(g.result + 'P', 'log'); if (++n < 20) step(); }; }; step();
await done(tx2); await workerRun;
const log = await req(db.transaction('kv').objectStore('kv').get('log'));
// a readonly transaction in the worker runs next to a page readonly transaction
const ro = await rpc(w, "const tx = self.db.transaction('kv'); const r = await req(tx.objectStore('kv').get('log')); return r.length;");
w.terminate(); db.close();
return JSON.stringify({ log, ro });`)
	if got != `{"log":"PPPPPPPPPPPPPPPPPPPPWWWWW","ro":25}` && got != `{"log":"WWWWWPPPPPPPPPPPPPPPPPPPP","ro":25}` {
		t.Fatalf("transactions interleaved or lost updates: %s\nconsole:\n%s", got, consoleText(tab))
	}
}
