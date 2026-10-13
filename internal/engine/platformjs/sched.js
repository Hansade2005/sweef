// Prioritized Task Scheduling: scheduler.postTask / yield, TaskController, TaskSignal, TaskPriorityChangeEvent.
var PRIORITIES = ['user-blocking', 'user-visible', 'background'];
function checkPriority(p) {
  p = String(p);
  if (PRIORITIES.indexOf(p) < 0) throw new TypeError("The provided value '" + p + "' is not a valid enum value of type TaskPriority.");
  return p;
}
var $ = Symbol('task');

H.eventClass('TaskPriorityChangeEvent', { previousPriority: undefined }, null, 'previousPriority');

// ---- TaskSignal / TaskController ---------------------------------------------------------------------------------
function TaskSignal() { throw new TypeError('Illegal constructor'); }
function makeSignal(priority) {
  var s = Object.create(TaskSignal.prototype);
  g.EventTarget.call(s);
  s.aborted = false; s.reason = undefined;
  Object.defineProperty(s, $, { value: { priority: priority, changing: false, dependents: [] } });
  return s;
}
Object.setPrototypeOf(TaskSignal.prototype, g.AbortSignal.prototype);
Object.setPrototypeOf(TaskSignal, g.AbortSignal);
H.attrs(TaskSignal.prototype, {
  priority: function () { var s = this !== null && typeof this === 'object' ? this[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s.priority; }
});
H.handlers(TaskSignal.prototype, ['prioritychange']);
H.define(TaskSignal, 'any', function any(signals, init) {
  var list = Array.from(signals);
  var priority = init && init.priority !== undefined ? checkPriority(init.priority) : null, src = null;
  if (priority === null) {
    for (var i = 0; i < list.length; i++) if (list[i] instanceof TaskSignal) { src = list[i]; break; }
    priority = src ? src[$].priority : 'user-visible';
  }
  var s = makeSignal(priority);
  var abortFn = g.AbortSignal.any(list);
  function fin() { s.aborted = true; s.reason = abortFn.reason; s.dispatchEvent(new g.Event('abort')); }
  if (abortFn.aborted) { s.aborted = true; s.reason = abortFn.reason; } else abortFn.addEventListener('abort', fin);
  if (!(init && init.priority !== undefined) && src) {
    src[$].dependents.push(s);
  }
  return s;
});
H.tag(TaskSignal, 'TaskSignal');

function setSignalPriority(sig, p) {
  var st = sig[$];
  if (st.changing) throw H.exc('Cannot change priority while a prioritychange event is dispatching.', 'NoModificationAllowedError');
  if (st.priority === p) return;
  var prev = st.priority;
  st.priority = p;
  st.changing = true;
  try { sig.dispatchEvent(new g.TaskPriorityChangeEvent('prioritychange', { previousPriority: prev })); } finally { st.changing = false; }
  st.dependents.forEach(function (d) { if (!d[$].fixed) setSignalPriority(d, p); });
  requeue(sig);
}

function TaskController(init) {
  if (!new.target) throw new TypeError("Failed to construct 'TaskController': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  var p = init && init.priority !== undefined ? checkPriority(init.priority) : 'user-visible';
  Object.defineProperty(this, 'signal', { value: makeSignal(p), enumerable: true });
}
Object.setPrototypeOf(TaskController.prototype, g.AbortController.prototype);
Object.setPrototypeOf(TaskController, g.AbortController);
H.define(TaskController.prototype, 'setPriority', function setPriority(priority) {
  if (!this || !this.signal || !this.signal[$]) throw new TypeError('Illegal invocation');
  if (arguments.length < 1) throw new TypeError("Failed to execute 'setPriority' on 'TaskController': 1 argument required, but only 0 present.");
  setSignalPriority(this.signal, checkPriority(priority));
});
H.tag(TaskController, 'TaskController');

// ---- the scheduler ---------------------------------------------------------------------------------------------------
var queues = { 'user-blocking': [], 'user-visible': [], 'background': [] }, pumping = false, currentPriority = null, seq = 0;
function effective(task) {
  if (task.explicit) return task.explicit;
  if (task.signal && task.signal[$]) return task.signal[$].priority;
  return 'user-visible';
}
function requeue(sig) {
  PRIORITIES.forEach(function (p) {
    queues[p] = queues[p].filter(function (t) {
      if (t.signal === sig && effective(t) !== p) { queues[effective(t)].push(t); queues[effective(t)].sort(function (a, b) { return a.seq - b.seq; }); return false; }
      return true;
    });
  });
}
function schedulePump() {
  if (pumping) return;
  pumping = true;
  setTimeout(pump, 0);
}
function pump() {
  pumping = false;
  for (var i = 0; i < PRIORITIES.length; i++) {
    var q = queues[PRIORITIES[i]];
    if (q.length) {
      var t = q.shift();
      runTask(t, PRIORITIES[i]);
      break;
    }
  }
  if (PRIORITIES.some(function (p) { return queues[p].length; })) schedulePump();
}
function runTask(t, priority) {
  t.done = true;
  if (t.signal && t.abortListener) t.signal.removeEventListener('abort', t.abortListener);
  var prev = currentPriority;
  currentPriority = priority;
  try { t.resolve(t.callback()); } catch (e) { t.reject(e); } finally { currentPriority = prev; }
}
function enqueue(task, front) {
  task.seq = front ? -(++seq) : ++seq;
  var q = queues[effective(task)];
  q.push(task);
  q.sort(function (a, b) { return a.seq - b.seq; });
  schedulePump();
}
function post(callback, options, isYield) {
  options = options === undefined || options === null ? {} : options;
  var signal = options.signal;
  if (signal !== undefined && !(signal instanceof g.AbortSignal)) return Promise.reject(new TypeError("Failed to execute 'postTask' on 'Scheduler': Failed to read the 'signal' property from 'SchedulerPostTaskOptions': Failed to convert value to 'AbortSignal'."));
  var explicit = options.priority !== undefined ? checkPriority(options.priority) : null;
  var delay = options.delay === undefined ? 0 : Math.max(0, Math.trunc(Number(options.delay)) || 0);
  if (signal && signal.aborted) return Promise.reject(signal.reason);
  var t = { callback: callback, signal: signal, explicit: explicit, done: false };
  t.promise = new Promise(function (res, rej) { t.resolve = res; t.reject = rej; });
  if (isYield && !explicit && !signal && currentPriority) t.explicit = currentPriority;
  if (signal) {
    t.abortListener = function () {
      if (t.done) return;
      t.done = true;
      PRIORITIES.forEach(function (p) { queues[p] = queues[p].filter(function (x) { return x !== t; }); });
      if (t.timer) clearTimeout(t.timer);
      t.reject(signal.reason);
    };
    signal.addEventListener('abort', t.abortListener);
  }
  if (delay > 0) t.timer = setTimeout(function () { t.timer = null; if (!t.done) enqueue(t, false); }, delay);
  else enqueue(t, isYield);
  return t.promise;
}
function Scheduler() { throw new TypeError('Illegal constructor'); }
H.define(Scheduler.prototype, 'postTask', function postTask(callback, options) {
  if (typeof callback !== 'function') return Promise.reject(new TypeError("Failed to execute 'postTask' on 'Scheduler': The callback provided as parameter 1 is not a function."));
  return post(callback, options, false);
});
H.define(Scheduler.prototype, 'yield', function (options) { return post(function () { }, options, true); });
H.tag(Scheduler, 'Scheduler');
H.glob('Scheduler', Scheduler);
H.glob('TaskSignal', TaskSignal);
H.glob('TaskController', TaskController);
Object.defineProperty(g, 'scheduler', { value: Object.create(Scheduler.prototype), writable: true, configurable: true, enumerable: true });
