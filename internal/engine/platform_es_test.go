package engine

import "testing"

func TestECMAScriptBuiltins(t *testing.T) {
	tab := platformTab(t)
	runJSCases(t, tab, []jsCase{
		// Natively present in QuickJS: guard them so a QuickJS downgrade is noticed.
		{"Set methods", `const a = new Set([1, 2, 3]), b = new Set([3, 4]); return [[...a.union(b)], [...a.intersection(b)], [...a.difference(b)], [...a.symmetricDifference(b)], a.isSubsetOf(new Set([1, 2, 3, 4])), a.isSupersetOf(new Set([1])), a.isDisjointFrom(new Set([9]))];`, `[[1,2,3,4],[3],[1,2],[1,2,4],true,true,true]`},
		{"Iterator helpers", `function* g() { yield 1; yield 2; yield 3; yield 4; } return [g().map(x => x * 2).toArray(), g().filter(x => x % 2).toArray(), g().take(2).toArray(), g().drop(3).toArray(), g().flatMap(x => [x, x]).take(3).toArray(), g().reduce((a, b) => a + b), g().some(x => x > 3), g().every(x => x > 0), g().find(x => x > 2), Iterator.from([5, 6]).toArray()];`, `[[2,4,6,8],[1,3],[1,2],[4],[1,1,2],10,true,true,3,[5,6]]`},
		{"Object.groupBy and Map.groupBy", `const o = Object.groupBy([1, 2, 3, 4], x => x % 2 ? 'odd' : 'even'); const m = Map.groupBy([1, 2, 3], x => x > 1); return [o.odd, o.even, Object.getPrototypeOf(o), m.get(true)];`, `[[1,3],[2,4],null,[2,3]]`},
		{"Promise.withResolvers and try", `const w = Promise.withResolvers(); w.resolve(7); return [await w.promise, await Promise.try(() => 8)];`, `[7,8]`},
		{"change-array-by-copy", `const a = [3, 1, 2]; return [a.toSorted(), a.toReversed(), a.toSpliced(0, 1), a.with(0, 9), a];`, `[[1,2,3],[2,1,3],[1,2],[9,1,2],[3,1,2]]`},
		{"string and buffer helpers", `const b = new ArrayBuffer(8); const t = b.transfer(4); return ['a\ud800'.isWellFormed(), 'a\ud800'.toWellFormed().charCodeAt(1), b.detached, t.byteLength];`, `[false,65533,true,4]`},
		{"regexp v flag is native", `return [new RegExp('[\\p{L}--[a-z]]', 'v').test('A'), new RegExp('[\\p{L}--[a-z]]', 'v').test('a')];`, `[true,false]`},
		{"typed array base64 and hex", `return [new Uint8Array([104, 105]).toBase64(), Uint8Array.fromBase64('aGk=')[1], new Uint8Array([255]).toHex(), Uint8Array.fromHex('0aff')[1]];`, `["aGk=",105,"ff",255]`},
		{"RegExp.escape and Error.isError", `return [RegExp.escape('a.b'), Error.isError(new TypeError()), Error.isError({})];`, `["\\x61\\.b",true,false]`},

		{"Array.fromAsync async iterable", `async function* g() { yield 1; yield 2; } return await Array.fromAsync(g());`, `[1,2]`},
		{"Array.fromAsync sync iterable awaits values", `return await Array.fromAsync([Promise.resolve(1), 2, Promise.resolve(3)]);`, `[1,2,3]`},
		{"Array.fromAsync array-like", `return await Array.fromAsync({ length: 3, 0: 'a', 1: Promise.resolve('b'), 2: 'c' });`, `["a","b","c"]`},
		{"Array.fromAsync mapper", `return await Array.fromAsync([1, 2, 3], async (x, i) => x * 10 + i);`, `[10,21,32]`},
		{"Array.fromAsync mapper thisArg", `return await Array.fromAsync([1], function (x) { return this.k + x; }, { k: 5 });`, `[6]`},
		{"Array.fromAsync subclass constructor", `class A extends Array { } const r = await Array.fromAsync.call(A, [1, 2]); return [r instanceof A, r.length];`, `[true,2]`},
		{"Array.fromAsync returns a promise and rejects", `const p = Array.fromAsync(null); const isP = p instanceof Promise; try { await p; } catch (e) { return [isP, e.name]; }`, `[true,"TypeError"]`},
		{"Array.fromAsync bad mapper", `try { await Array.fromAsync([1], 5); } catch (e) { return e.name; }`, `"TypeError"`},
		{"Array.fromAsync closes iterator on mapper error", `let closed = false; const it = { [Symbol.asyncIterator]() { return { next: async () => ({ value: 1, done: false }), return: async () => { closed = true; return {}; } }; } }; try { await Array.fromAsync(it, () => { throw new Error('x'); }); } catch (e) { return [e.message, closed]; }`, `["x",true]`},
		{"Array.fromAsync length and name", `return [Array.fromAsync.length, Array.fromAsync.name];`, `[1,"fromAsync"]`},

		{"Symbol.dispose is a symbol", `return [typeof Symbol.dispose, typeof Symbol.asyncDispose, Symbol.dispose.description];`, `["symbol","symbol","Symbol.dispose"]`},
		{"DisposableStack order and use", `const log = []; const s = new DisposableStack(); s.use({ [Symbol.dispose]() { log.push('use'); } }); s.defer(() => log.push('defer')); s.adopt(5, v => log.push('adopt' + v)); s.dispose(); return [log, s.disposed];`, `[["adopt5","defer","use"],true]`},
		{"DisposableStack errors become SuppressedError", `const s = new DisposableStack(); s.defer(() => { throw new Error('first'); }); s.defer(() => { throw new Error('second'); }); try { s.dispose(); } catch (e) { return [e.name, e.error.message, e.suppressed.message, e instanceof Error]; }`, `["SuppressedError","first","second",true]`},
		{"DisposableStack move and double dispose", `const s = new DisposableStack(); let n = 0; s.defer(() => n++); const m = s.move(); s.dispose(); m.dispose(); m.dispose(); return [n, s.disposed, m.disposed];`, `[1,true,true]`},
		{"DisposableStack use after dispose", `const s = new DisposableStack(); s.dispose(); try { s.defer(() => { }); } catch (e) { return e.name; }`, `"ReferenceError"`},
		{"DisposableStack use of non disposable", `const s = new DisposableStack(); try { s.use({}); } catch (e) { return e.name; }`, `"TypeError"`},
		{"AsyncDisposableStack", `const log = []; const s = new AsyncDisposableStack(); s.use({ async [Symbol.asyncDispose]() { log.push('a'); } }); s.use({ [Symbol.dispose]() { log.push('s'); } }); s.defer(async () => { await 0; log.push('d'); }); await s.disposeAsync(); return [log, s.disposed];`, `[["d","s","a"],true]`},
		{"disposable tags", `return [String(new DisposableStack()), String(new AsyncDisposableStack()), typeof DisposableStack.prototype[Symbol.dispose], DisposableStack.prototype[Symbol.dispose] === DisposableStack.prototype.dispose];`, `["[object DisposableStack]","[object AsyncDisposableStack]","function",true]`},
		{"SuppressedError ctor", `const e = new SuppressedError('a', 'b', 'msg'); return [e.error, e.suppressed, e.message, e.name, SuppressedError('x', 'y').suppressed];`, `["a","b","msg","SuppressedError","y"]`},

		{"Atomics.waitAsync not-equal and timeout", `const ia = new Int32Array(new SharedArrayBuffer(8)); const a = Atomics.waitAsync(ia, 0, 1); const b = Atomics.waitAsync(ia, 0, 0, 0); return [a.async, a.value, b.async, b.value];`, `[false,"not-equal",false,"timed-out"]`},
		{"Atomics.waitAsync notified", `const ia = new Int32Array(new SharedArrayBuffer(8)); const w = Atomics.waitAsync(ia, 0, 0); const n = Atomics.notify(ia, 0); return [w.async, n, await w.value];`, `[true,1,"ok"]`},
		{"Atomics.waitAsync times out", `const ia = new Int32Array(new SharedArrayBuffer(8)); const w = Atomics.waitAsync(ia, 1, 0, 10); return await w.value;`, `"timed-out"`},
		{"Atomics.waitAsync rejects plain buffers", `try { Atomics.waitAsync(new Int32Array(2), 0, 0); } catch (e) { return e.name; }`, `"TypeError"`},

		{"Intl.Locale basics", `const l = new Intl.Locale('EN_us'); return [l.language, l.region, l.script, l.baseName, l.toString()];`, `["en","US",null,"en-US","en-US"]`},
		{"Intl.Locale unicode extensions", `const l = new Intl.Locale('de-DE-u-ca-gregory-hc-h23-kn-nu-latn'); return [l.calendar, l.hourCycle, l.numeric, l.numberingSystem, l.baseName, l.toString()];`, `["gregory","h23",true,"latn","de-DE","de-DE-u-ca-gregory-hc-h23-kn-nu-latn"]`},
		{"Intl.Locale options override", `const l = new Intl.Locale('en', { region: 'gb', script: 'latn', calendar: 'buddhist', numeric: true }); return [l.toString(), l.numeric];`, `["en-Latn-GB-u-ca-buddhist-kn",true]`},
		{"Intl.Locale maximize minimize", `const a = new Intl.Locale('en').maximize().toString(); const b = new Intl.Locale('zh-TW').maximize().toString(); const c = new Intl.Locale('en-Latn-US').minimize().toString(); const d = new Intl.Locale('zh-Hant-TW').minimize().toString(); return [a, b, c, d];`, `["en-Latn-US","zh-Hant-TW","en","zh-TW"]`},
		{"Intl.Locale invalid", `const out = []; for (const t of ['', 'x', 'en-', '1234']) { try { new Intl.Locale(t); out.push('ok'); } catch (e) { out.push(e.name); } } try { new Intl.Locale(); } catch (e) { out.push(e.name); } return out;`, `["RangeError","RangeError","RangeError","RangeError","TypeError"]`},
		{"Intl.Locale tag", `return [String(new Intl.Locale('en')), Object.prototype.toString.call(new Intl.Locale('en'))];`, `["en","[object Intl.Locale]"]`},

		{"Error.captureStackTrace", `const o = {}; Error.captureStackTrace(o); return typeof o.stack;`, `"string"`},
	})
}

func TestScheduler(t *testing.T) {
	tab := platformTab(t)
	runJSCases(t, tab, []jsCase{
		{"postTask returns the result", `return await scheduler.postTask(() => 42);`, `42`},
		{"postTask rejects on throw", `try { await scheduler.postTask(() => { throw new RangeError('x'); }); } catch (e) { return e.name; }`, `"RangeError"`},
		{"postTask rejects non-function", `try { await scheduler.postTask(5); } catch (e) { return e.name; }`, `"TypeError"`},
		{"priorities run in order", `const log = []; const ps = [scheduler.postTask(() => log.push('bg'), { priority: 'background' }), scheduler.postTask(() => log.push('uv')), scheduler.postTask(() => log.push('ub'), { priority: 'user-blocking' })]; await Promise.all(ps); return log;`, `["ub","uv","bg"]`},
		{"same priority is fifo", `const log = []; await Promise.all([1, 2, 3].map(i => scheduler.postTask(() => log.push(i)))); return log;`, `[1,2,3]`},
		{"delay", `const t0 = performance.now(); await scheduler.postTask(() => 0, { delay: 30 }); return performance.now() - t0 >= 25;`, `true`},
		{"abort rejects with the reason", `const c = new AbortController(); const p = scheduler.postTask(() => 1, { signal: c.signal }); c.abort(); try { await p; } catch (e) { return e.name; }`, `"AbortError"`},
		{"already aborted signal", `const c = new AbortController(); c.abort('why'); try { await scheduler.postTask(() => 1, { signal: c.signal }); } catch (e) { return e; }`, `"why"`},
		{"aborted task never runs", `let ran = false; const c = new AbortController(); const p = scheduler.postTask(() => { ran = true; }, { signal: c.signal }); c.abort(); try { await p; } catch (e) { } await scheduler.postTask(() => 0); return ran;`, `false`},
		{"invalid priority", `try { await scheduler.postTask(() => 1, { priority: 'urgent' }); } catch (e) { return e.name; }`, `"TypeError"`},
		{"TaskController signal priority", `const c = new TaskController({ priority: 'background' }); return [c.signal.priority, c.signal instanceof TaskSignal, c.signal instanceof AbortSignal, c instanceof AbortController, new TaskController().signal.priority];`, `["background",true,true,true,"user-visible"]`},
		{"setPriority fires prioritychange", `const c = new TaskController(); const seen = []; c.signal.onprioritychange = e => seen.push([e.type, e.previousPriority, c.signal.priority, e instanceof TaskPriorityChangeEvent]); c.setPriority('background'); c.setPriority('background'); return seen;`, `[["prioritychange","user-visible","background",true]]`},
		{"signal priority used by postTask", `const log = []; const lo = new TaskController({ priority: 'background' }); const a = scheduler.postTask(() => log.push('lo'), { signal: lo.signal }); const b = scheduler.postTask(() => log.push('uv')); await Promise.all([a, b]); return log;`, `["uv","lo"]`},
		{"setPriority requeues pending tasks", `const log = []; const c = new TaskController({ priority: 'background' }); const a = scheduler.postTask(() => log.push('moved'), { signal: c.signal }); const b = scheduler.postTask(() => log.push('uv')); const d = scheduler.postTask(() => log.push('bg'), { priority: 'background' }); c.setPriority('user-blocking'); await Promise.all([a, b, d]); return log;`, `["moved","uv","bg"]`},
		{"TaskController.abort", `const c = new TaskController(); const p = scheduler.postTask(() => 1, { signal: c.signal }); c.abort(); try { await p; } catch (e) { return [e.name, c.signal.aborted]; }`, `["AbortError",true]`},
		{"TaskPriorityChangeEvent needs previousPriority", `try { new TaskPriorityChangeEvent('prioritychange'); } catch (e) { return [e.name, new TaskPriorityChangeEvent('x', { previousPriority: 'background' }).previousPriority]; }`, `["TypeError","background"]`},
		{"scheduler.yield", `const log = []; const a = scheduler.postTask(() => log.push('task')); await scheduler.yield(); log.push('after-yield'); await a; return log;`, `["after-yield","task"]`},
		{"scheduler.yield runs before same priority tasks queued later", `const log = []; (async () => { await scheduler.postTask(async () => { }); })(); const p = scheduler.postTask(async () => { scheduler.postTask(() => log.push('later')); await scheduler.yield(); log.push('resumed'); }); await p; await scheduler.postTask(() => 0); return log;`, `["resumed","later"]`},
		{"tags", `return [String(scheduler), typeof Scheduler, String(new TaskController()), 'postTask' in scheduler, 'yield' in scheduler];`, `["[object Scheduler]","function","[object TaskController]",true,true]`},
	})
}
