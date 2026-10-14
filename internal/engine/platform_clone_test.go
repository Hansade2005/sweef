package engine

import "testing"

func TestStructuredCloneAndKeyStorage(t *testing.T) {
	tab := platformTab(t)
	pre := cryptoHelpers
	runJSCases(t, tab, []jsCase{
		{"clone a CryptoKey and use the copy", pre + `const k = await S.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']); const c = structuredClone(k); const iv = new Uint8Array(12); const ct = await S.encrypt({ name: 'AES-GCM', iv }, k, new Uint8Array([5])); const pt = new Uint8Array(await S.decrypt({ name: 'AES-GCM', iv }, c, ct)); let ex; try { await S.exportKey('raw', c); } catch (e) { ex = e.name; } return [c instanceof CryptoKey, c !== k, c.extractable, c.algorithm.length, Array.from(pt), ex];`, `[true,true,false,256,[5],"InvalidAccessError"]`},
		{"clone an EC key pair inside an object", pre + `const kp = await S.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']); const c = structuredClone({ kp, n: 1 }); const sig = await S.sign({ name: 'ECDSA', hash: 'SHA-256' }, c.kp.privateKey, new Uint8Array(1)); return [c.n, c.kp.publicKey.type, await S.verify({ name: 'ECDSA', hash: 'SHA-256' }, kp.publicKey, sig, new Uint8Array(1))];`, `[1,"public",true]`},
		{"a CryptoKey survives IndexedDB", pre + `
const open = () => new Promise((res, rej) => { const r = indexedDB.open('cryptokeys', 1); r.onupgradeneeded = () => r.result.createObjectStore('k'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
const req = (r) => new Promise((res, rej) => { r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
const db = await open();
const key = await S.generateKey({ name: 'HMAC', hash: 'SHA-256' }, false, ['sign', 'verify']);
const m = new Uint8Array([1, 2, 3]);
const sig = hex(await S.sign('HMAC', key, m));
await req(db.transaction('k', 'readwrite').objectStore('k').put({ key }, 'a'));
const got = await req(db.transaction('k').objectStore('k').get('a'));
return [got.key instanceof CryptoKey, got.key.extractable, hex(await S.sign('HMAC', got.key, m)) === sig];`, `[true,false,true]`},
		{"structuredClone value types", `
class P { constructor() { this.a = 1; } get g() { return 2; } }
const o = { d: new Date(5), r: /x/gi, m: new Map([[1, { z: 1 }]]), s: new Set([1, 2]), b: 10n, u: undefined, n: null, nan: NaN, neg0: -0, arr: [1, , 3], big: new Uint8Array([1, 2, 3]), p: new P(), e: new RangeError('boom'), w: new Number(4), str: new String('s') };
o.self = o;
const c = structuredClone(o);
return [c.d.getTime(), c.r.source, c.r.flags, c.m.get(1).z, c.s.has(2), c.b === 10n, 'u' in c, c.n, Number.isNaN(c.nan), Object.is(c.neg0, -0), c.arr.length, 1 in c.arr, Array.from(c.big), Object.getPrototypeOf(c.p) === Object.prototype, c.p.a, c.e instanceof RangeError, c.e.message, typeof c.w, c.w.valueOf(), c.self === c, c.str.valueOf()];`, `[5,"x","gi",1,true,true,true,null,true,true,3,false,[1,2,3],true,1,true,"boom","object",4,true,"s"]`},
		{"structuredClone keeps views sharing one buffer", `const buf = new ArrayBuffer(8); const c = structuredClone({ a: new Uint8Array(buf, 0, 4), b: new Uint8Array(buf, 4, 4), buf }); c.a[0] = 9; return [c.buf !== buf, new Uint8Array(c.buf)[0], c.a.buffer === c.b.buffer, c.b.byteOffset];`, `[true,9,true,4]`},
		{"structuredClone errors", `const out = []; for (const v of [1, function () {}, Symbol('x'), document.body, Promise.resolve(), new WeakMap()]) { try { structuredClone(v); out.push('ok'); } catch (e) { out.push(e.name); } } return out;`, `["ok","DataCloneError","DataCloneError","DataCloneError","DataCloneError","DataCloneError"]`},
		{"structuredClone DOMException, ImageData, Blob", `const c = structuredClone({ d: new DOMException('m', 'NotFoundError'), i: new ImageData(new Uint8ClampedArray(8), 2, 1), b: new Blob(['abc'], { type: 'text/plain' }) }); return [c.d instanceof DOMException, c.d.name, c.d.message, c.i.width, c.i.height, c.i.data.length, c.b.size, c.b.type, await c.b.text()];`, `[true,"NotFoundError","m",2,1,8,3,"text/plain","abc"]`},
		{"structuredClone transfer detaches", `const b = new ArrayBuffer(4); const c = structuredClone(b, { transfer: [b] }); return [b.byteLength, c.byteLength];`, `[0,4]`},
	})
}
