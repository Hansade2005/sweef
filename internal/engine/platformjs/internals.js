// ElementInternals and element.attachInternals(): form-associated custom elements, constraint
// validation, CustomStateSet and ARIA defaults.
var $ = Symbol('internals');
var NS_HTML = 'http://www.w3.org/1999/xhtml';
var internalsOf = new WeakMap(); // element -> ElementInternals

function CustomStateSet() { throw new TypeError('Illegal constructor'); }
CustomStateSet.prototype = Object.create(Set.prototype, { constructor: { value: CustomStateSet, writable: true, configurable: true } });
Object.defineProperty(CustomStateSet.prototype, Symbol.toStringTag, { value: 'CustomStateSet', configurable: true });
H.define(CustomStateSet.prototype, 'add', function add(v) { Set.prototype.add.call(this, String(v)); return this; });
function newStateSet() { var s = new Set(); Object.setPrototypeOf(s, CustomStateSet.prototype); return s; }

var FLAGS = ['valueMissing', 'typeMismatch', 'patternMismatch', 'tooLong', 'tooShort', 'rangeUnderflow', 'rangeOverflow', 'stepMismatch', 'badInput', 'customError'];
function ValidityState() { throw new TypeError('Illegal constructor'); }
FLAGS.forEach(function (f) { Object.defineProperty(ValidityState.prototype, f, { get: function () { return !!this[$][f]; }, enumerable: true, configurable: true }); });
Object.defineProperty(ValidityState.prototype, 'valid', { get: function () { var s = this[$]; return !FLAGS.some(function (f) { return s[f]; }); }, enumerable: true, configurable: true });
H.tag(ValidityState, 'ValidityState');
function newValidity() { var v = Object.create(ValidityState.prototype); Object.defineProperty(v, $, { value: {}, writable: true }); return v; }

function ElementInternals() { throw new TypeError('Illegal constructor'); }
function slot(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
function formAssociated(s) { var C = s.el.constructor; return !!(C && C.formAssociated); }
function needFA(s, what) {
  if (!formAssociated(s)) throw H.exc("Failed to execute '" + what + "' on 'ElementInternals': The target element is not a form-associated custom element.", 'NotSupportedError');
}
function formOf(el) {
  var id = el.getAttribute('form');
  if (id) { var f = el.ownerDocument.getElementById(id); return f && f.localName === 'form' ? f : null; }
  for (var p = el.parentNode; p; p = p.parentNode) if (p.nodeType === 1 && p.localName === 'form') return p;
  return null;
}
function barredFromValidation(s) {
  var el = s.el;
  if (el.hasAttribute('disabled') || el.hasAttribute('readonly')) return true;
  for (var p = el.parentNode; p; p = p.parentNode) if (p.nodeType === 1 && p.localName === 'fieldset' && p.hasAttribute('disabled')) return true;
  return false;
}
Object.defineProperties(ElementInternals.prototype, {
  shadowRoot: { get: function () { var s = slot(this); return s.el.shadowRoot || null; }, enumerable: true, configurable: true },
  form: { get: function () { var s = slot(this); return formAssociated(s) ? formOf(s.el) : null; }, enumerable: true, configurable: true },
  states: { get: function () { return slot(this).states; }, enumerable: true, configurable: true },
  willValidate: { get: function () { var s = slot(this); needFA(s, 'willValidate'); return !barredFromValidation(s); }, enumerable: true, configurable: true },
  validity: { get: function () { var s = slot(this); needFA(s, 'validity'); return s.validity; }, enumerable: true, configurable: true },
  validationMessage: { get: function () { var s = slot(this); needFA(s, 'validationMessage'); return s.message; }, enumerable: true, configurable: true },
  labels: {
    get: function () {
      var s = slot(this); needFA(s, 'labels');
      var el = s.el, doc = el.ownerDocument, out = [];
      Array.prototype.forEach.call(doc.getElementsByTagName('label'), function (l) {
        var f = l.getAttribute('for');
        if (f !== null) { if (el.id && f === el.id) out.push(l); return; }
        for (var c = el.parentNode; c; c = c.parentNode) if (c === l) { out.push(l); return; }
      });
      return out;
    }, enumerable: true, configurable: true
  }
});
H.define(ElementInternals.prototype, 'setFormValue', function setFormValue(value, state) {
  var s = slot(this); needFA(s, 'setFormValue');
  if (value !== null && value !== undefined && typeof value !== 'string' && !(g.File && value instanceof g.File) && !(g.FormData && value instanceof g.FormData)) throw new TypeError("Failed to execute 'setFormValue' on 'ElementInternals': parameter 1 is not of type 'File or USVString or FormData'.");
  s.value = value === undefined ? null : value;
  s.state = arguments.length > 1 ? state : s.value;
});
H.define(ElementInternals.prototype, 'setValidity', function setValidity(flags, message, anchor) {
  var s = slot(this); needFA(s, 'setValidity');
  if (flags === undefined || flags === null) flags = {};
  if (typeof flags !== 'object') throw new TypeError("Failed to execute 'setValidity' on 'ElementInternals': The provided value is not of type 'ValidityStateFlags'.");
  var set = {}, any = false;
  FLAGS.forEach(function (f) { if (flags[f]) { set[f] = true; any = true; } });
  if (any) {
    if (message === undefined || message === '') throw new TypeError("Failed to execute 'setValidity' on 'ElementInternals': The second argument should not be empty if one or more flags in the first argument are true.");
    if (anchor !== undefined && anchor !== null) {
      if (!anchor || anchor.nodeType !== 1) throw new TypeError("Failed to execute 'setValidity' on 'ElementInternals': parameter 3 is not of type 'HTMLElement'.");
      var inside = false;
      for (var p = anchor.parentNode; p; p = p.parentNode) if (p === s.el) inside = true;
      if (!inside) throw H.exc("Failed to execute 'setValidity' on 'ElementInternals': The anchor element is not a shadow-including descendant of the element.", 'NotFoundError');
    }
  }
  s.validity[$] = set;
  s.message = any ? String(message) : '';
  s.anchor = any ? anchor || null : null;
});
H.define(ElementInternals.prototype, 'checkValidity', function checkValidity() {
  var s = slot(this); needFA(s, 'checkValidity');
  if (barredFromValidation(s) || s.validity.valid) return true;
  s.el.dispatchEvent(new g.Event('invalid', { cancelable: true }));
  return false;
});
H.define(ElementInternals.prototype, 'reportValidity', function reportValidity() {
  var s = slot(this); needFA(s, 'reportValidity');
  return this.checkValidity();
});

// ARIA reflection: the defaults an element exposes when it has no ARIA attribute of its own.
var ARIA = 'ariaAtomic ariaAutoComplete ariaBusy ariaChecked ariaColCount ariaColIndex ariaColSpan ariaCurrent ariaDescription ariaDisabled ariaExpanded ariaHasPopup ariaHidden ariaInvalid ariaKeyShortcuts ariaLabel ariaLevel ariaLive ariaModal ariaMultiLine ariaMultiSelectable ariaOrientation ariaPlaceholder ariaPosInSet ariaPressed ariaReadOnly ariaRelevant ariaRequired ariaRoleDescription ariaRowCount ariaRowIndex ariaRowSpan ariaSelected ariaSetSize ariaSort ariaValueMax ariaValueMin ariaValueNow ariaValueText role'.split(' ');
ARIA.forEach(function (name) {
  Object.defineProperty(ElementInternals.prototype, name, {
    get: function () { var v = slot(this).aria[name]; return v === undefined ? null : v; },
    set: function (v) { slot(this).aria[name] = v === null || v === undefined ? null : String(v); },
    enumerable: true, configurable: true
  });
});
'ariaActiveDescendantElement ariaControlsElements ariaDescribedByElements ariaDetailsElements ariaErrorMessageElements ariaFlowToElements ariaLabelledByElements ariaOwnsElements'.split(' ').forEach(function (name) {
  Object.defineProperty(ElementInternals.prototype, name, {
    get: function () { var v = slot(this).aria[name]; return v === undefined ? null : v; },
    set: function (v) { slot(this).aria[name] = v === null || v === undefined ? null : v; },
    enumerable: true, configurable: true
  });
});
H.tag(ElementInternals, 'ElementInternals');

H.define(g.Element.prototype, 'attachInternals', function attachInternals() {
  var el = this;
  if (el.nodeType !== 1 || el.namespaceURI !== NS_HTML) throw H.exc("Failed to execute 'attachInternals' on 'HTMLElement': Unable to attach ElementInternals to non-custom elements.", 'NotSupportedError');
  if (!/^[a-z][.0-9_a-z]*-[-.0-9_a-z]*$/.test(el.localName) || el.hasAttribute('is')) throw H.exc("Failed to execute 'attachInternals' on 'HTMLElement': Unable to attach ElementInternals to non-custom elements.", 'NotSupportedError');
  var def = g.customElements.get(el.localName);
  if (def && el.constructor !== def && !(el instanceof def)) throw H.exc("Failed to execute 'attachInternals' on 'HTMLElement': Unable to attach ElementInternals to a customized built-in or non-custom element.", 'NotSupportedError');
  if (def && def.disabledFeatures && Array.prototype.indexOf.call(def.disabledFeatures, 'internals') >= 0) throw H.exc("Failed to execute 'attachInternals' on 'HTMLElement': ElementInternals is disabled for this element.", 'NotSupportedError');
  if (internalsOf.has(el)) throw H.exc("Failed to execute 'attachInternals' on 'HTMLElement': ElementInternals for the specified element was already attached.", 'NotSupportedError');
  var ei = Object.create(ElementInternals.prototype);
  Object.defineProperty(ei, $, { value: { el: el, states: newStateSet(), validity: newValidity(), message: '', anchor: null, value: null, state: null, aria: {} } });
  internalsOf.set(el, ei);
  return ei;
});

// Form participation: FormData(form) includes the submission values of form-associated custom
// elements, and form.reset() runs formResetCallback.
function associatedElements(form) {
  var out = [];
  Array.prototype.forEach.call(form.ownerDocument.querySelectorAll('*'), function (el) {
    if (!/-/.test(el.localName)) return;
    var ei = internalsOf.get(el);
    if (!ei || !formAssociated(ei[$])) return;
    if (formOf(el) === form) out.push(el);
  });
  return out;
}
g.__swHooks.formData = function (form, list) {
  associatedElements(form).forEach(function (el) {
    var s = internalsOf.get(el)[$], name = el.getAttribute('name');
    if (!name || el.hasAttribute('disabled') || s.value === null) return;
    if (typeof s.value === 'string' || (g.File && s.value instanceof g.File)) list.push([name, s.value]);
    else if (g.FormData && s.value instanceof g.FormData) s.value.forEach(function (v, k) { list.push([k, v]); });
  });
};
var FP = g.HTMLFormElement && g.HTMLFormElement.prototype;
if (FP && FP.reset) {
  var origReset = FP.reset;
  H.define(FP, 'reset', function reset() {
    var r = origReset.apply(this, arguments);
    var form = this;
    associatedElements(form).forEach(function (el) { if (typeof el.formResetCallback === 'function') { try { el.formResetCallback(); } catch (e) { /* reported by the page */ } } });
    return r;
  });
}
H.glob('ElementInternals', ElementInternals);
H.glob('CustomStateSet', CustomStateSet);
if (H.missing(g, 'ValidityState')) H.glob('ValidityState', ValidityState);
