package engine

import "testing"

const xpathDOM = `document.body.innerHTML = '<div id="root" lang="en-US"><ul class="list"><li id="a" class="x">One</li><li id="b">Two <b>bold</b></li><li id="c" data-n="3">Three</li><!-- note --></ul><p>Para <span>1</span></p><p>Para <span>2</span></p><a href="/x">link</a></div>';
const X = (e, ctx, t, r) => document.evaluate(e, ctx || document, r || null, t === undefined ? 0 : t);
const ids = (e, ctx) => { const r = X(e, ctx, 7); const o = []; for (let i = 0; i < r.snapshotLength; i++) { const n = r.snapshotItem(i); o.push(n.id || n.nodeName || n.name); } return o; };
const num = (e, ctx) => X(e, ctx, 1).numberValue; const str = (e, ctx) => X(e, ctx, 2).stringValue; const bool = (e, ctx) => X(e, ctx, 3).booleanValue;
`

func TestXPath(t *testing.T) {
	tab := platformTab(t)
	pre := xpathDOM
	runJSCases(t, tab, []jsCase{
		{"descendant snapshot", pre + `return ids('//li');`, `["a","b","c"]`},
		{"count", pre + `return num('count(//li)');`, `3`},
		{"string value of element", pre + `return str('string(//li[2])');`, `"Two bold"`},
		{"last() and attribute", pre + `return str('//li[last()]/@id');`, `"c"`},
		{"position predicate", pre + `return ids('//li[position()>1]');`, `["b","c"]`},
		{"numeric predicate", pre + `return ids('//li[2]');`, `["b"]`},
		{"attribute equality number", pre + `return ids('//li[@data-n=3]');`, `["c"]`},
		{"attribute equality string", pre + `return ids("//li[@class='x']");`, `["a"]`},
		{"union in document order", pre + `return ids('//p[1] | //ul/li[1]');`, `["a","P"]`},
		{"parent step", pre + `return ids('//span/..');`, `["P","P"]`},
		{"following-sibling", pre + `return ids("//li[@id='b']/following-sibling::li");`, `["c"]`},
		{"preceding-sibling is reverse axis", pre + `return ids("//li[@id='c']/preceding-sibling::li[1]");`, `["b"]`},
		{"ancestor", pre + `return str("//li[@id='a']/ancestor::div/@id");`, `"root"`},
		{"ancestor-or-self count", pre + `return num('count(//b/ancestor-or-self::*)');`, `6`},
		{"preceding", pre + `return ids("//li[@id='c']/preceding::li");`, `["a","b"]`},
		{"following", pre + `return num('count(//ul/following::p)');`, `2`},
		{"descendant-or-self text()", pre + `return num("count(//li[@id='b']/descendant-or-self::text())");`, `2`},
		{"comment()", pre + `return [num('count(//comment())'), str('string(//comment())')];`, `[1," note "]`},
		{"case-insensitive html names", pre + `return num('count(//LI)');`, `3`},
		{"substring spec examples", pre + `return [str('substring("12345", 1.5, 2.6)'), str('substring("12345", 0, 3)'), str('substring("12345", -42, 1 div 0)'), str('substring("12345", 0 div 0, 3)'), str('substring("12345", 2)')];`, `["234","12","12345","","2345"]`},
		{"string functions", pre + `return [str('translate("bar","abc","ABC")'), str('translate("--aaa--","abc-","ABC")'), str('normalize-space("  a   b  ")'), num('string-length("hello")'), str('concat("a","b","c")'), str('substring-before("1999/04/01","/")'), str('substring-after("1999/04/01","/")')];`, `["BAr","AAA","a b",5,"abc","1999","04/01"]`},
		{"boolean functions", pre + `return [bool('contains("abc","bc")'), bool('starts-with("abc","ab")'), bool('not(false())'), bool('boolean("")'), bool('boolean(//li)'), bool('lang("en")'), bool('lang("fr")')];`, `[true,true,true,false,true,false,false]`},
		{"lang on context node", pre + `return [X('lang("en")', document.querySelector('span'), 3).booleanValue, X('lang("EN-us")', document.querySelector('span'), 3).booleanValue, X('lang("en-GB")', document.querySelector('span'), 3).booleanValue];`, `[true,true,false]`},
		{"number functions", pre + `return [num('number("  12 ")'), isNaN(num('number("x")')), num('sum(//li/@data-n)'), num('floor(2.7)'), num('ceiling(2.1)'), num('round(2.5)'), num('round(-2.5)'), num('1 + 2 * 3'), num('10 mod 3'), num('7 div 2'), num('-3 - -2')];`, `[12,true,3,2,3,3,-2,7,1,3.5,-1]`},
		{"infinity and NaN strings", pre + `return [str('string(1 div 0)'), str('string(-1 div 0)'), str('string(0 div 0)'), str('string(1000000000000000000000)'), str('string(0.5)'), str('string(-0)')];`, `["Infinity","-Infinity","NaN","1000000000000000000000","0.5","0"]`},
		{"comparisons", pre + `return [bool("//li = 'Three'"), bool("//li/@id != 'a'"), bool('count(//li) = 3'), bool('2 < 3'), bool("'a' < 'b'"), bool('true() = 1'), bool('//nothing = //li'), bool('//li/@data-n > 2')];`, `[true,true,true,true,false,true,false,true]`},
		{"id()", pre + `return ids('id("b c")');`, `["b","c"]`},
		{"name functions", pre + `return [str('name(//li)'), str('local-name(//ul)'), str('namespace-uri(//ul)'), str('name(//li/@id)')];`, `["li","ul","http://www.w3.org/1999/xhtml","id"]`},
		{"relative context and dot", pre + `const ul = document.querySelector('ul'); return [ids('li', ul), ids('.//b', ul), ids('..', ul), ids('.', ul), ids('./li[1]', ul)];`, `[["a","b","c"],["B"],["root"],["UL"],["a"]]`},
		{"absolute path from inner context", pre + `return ids('/html/body/div/ul/li', document.querySelector('b'));`, `["a","b","c"]`},
		{"attribute axis nodes", pre + `const r = X('//li[@id="c"]/@*', document, 7); return [r.snapshotLength, r.snapshotItem(0).nodeType, r.snapshotItem(0).name, r.snapshotItem(1).value];`, `[2,2,"id","3"]`},
		{"attribute as context", pre + `const a = document.querySelector('a').getAttributeNode('href'); return [str('string(.)', a), str('name(..)', a), num('count(ancestor::*)', a)];`, `["/x","a",4]`},
		{"result types any", pre + `return [X('//li', document).resultType, X('1+1', document).resultType, X('"a"', document).resultType, X('true()', document).resultType];`, `[4,1,2,3]`},
		{"result constants", pre + `return [XPathResult.NUMBER_TYPE, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, XPathResult.FIRST_ORDERED_NODE_TYPE, X('1', document, 1).NUMBER_TYPE];`, `[1,7,9,1]`},
		{"single node and null", pre + `return [X('//li[2]', document, 9).singleNodeValue.id, X('//nothing', document, 9).singleNodeValue, X('//li', document, 8).singleNodeValue.id];`, `["b",null,"a"]`},
		{"iterator", pre + `const r = X('//li', document, 5); const o = []; let n; while ((n = r.iterateNext())) o.push(n.id); return [o, r.iterateNext(), r.invalidIteratorState];`, `[["a","b","c"],null,false]`},
		{"iterator invalidated by mutation", pre + `const r = X('//li', document, 4); r.iterateNext(); document.body.appendChild(document.createElement('i')); try { r.iterateNext(); } catch (e) { return [r.invalidIteratorState, e.name]; }`, `[true,"InvalidStateError"]`},
		{"snapshot not live", pre + `const r = X('//li', document, 7); document.querySelector('#a').remove(); return [r.snapshotLength, r.snapshotItem(0).id, r.snapshotItem(5)];`, `[3,"a",null]`},
		{"wrong type access throws", pre + `const out = []; for (const f of [() => X('1', document, 1).stringValue, () => X('//li', document, 5).snapshotLength, () => X('1', document, 1).singleNodeValue, () => X('"a"', document, 7)]) { try { f(); out.push('ok'); } catch (e) { out.push(e.name + ':' + e.code); } } return out;`, `["TypeError:52","TypeError:52","TypeError:52","TypeError:52"]`},
		{"syntax error", pre + `const out = []; for (const e of ['//li[', '1 +', '$x', 'foo(', '//li[@]', 'nonesuch()']) { try { X(e); out.push('ok'); } catch (x) { out.push(x.name); } } return out;`, `["SyntaxError","SyntaxError","NamespaceError","SyntaxError","SyntaxError","SyntaxError"]`},
		{"unresolvable prefix", pre + `try { X('//svg:rect'); } catch (e) { return [e.name, e.code]; }`, `["NamespaceError",14]`},
		{"svg namespace match", pre + `document.body.insertAdjacentHTML('beforeend', '<svg><rect id="r"/></svg>'); const ns = 'http://www.w3.org/2000/svg'; const r = document.evaluate('//s:rect', document, p => ns, 9); const o = document.evaluate('//s:rect', document, { lookupNamespaceURI: () => ns }, 9); return [r.singleNodeValue && r.singleNodeValue.id, o.singleNodeValue && o.singleNodeValue.id, num('count(//rect)'), num('count(//*[local-name()="rect"])')];`, `["r","r",0,1]`},
		{"createNSResolver", pre + `const r = document.createNSResolver(document.documentElement); return [typeof r.lookupNamespaceURI, r.lookupNamespaceURI('xml')];`, `["function","http://www.w3.org/XML/1998/namespace"]`},
		{"XPathEvaluator and expression reuse", pre + `const ev = new XPathEvaluator(); const ex = ev.createExpression('count(li)', null); const ul = document.querySelector('ul'); const r1 = ex.evaluate(ul, 1).numberValue; const r2 = ev.evaluate('string(//li[1])', document, null, 2).stringValue; return [r1, r2, ex instanceof XPathExpression, ev instanceof XPathEvaluator, Object.prototype.toString.call(ex)];`, `[3,"One",true,true,"[object XPathExpression]"]`},
		{"document order of mixed axes", pre + `return ids('//li[@id="b"]/ancestor-or-self::* | //li[@id="a"]').join();`, `"HTML,BODY,root,UL,a,b"`},
		{"nested predicates", pre + `return ids('//ul[li[@data-n]]/li[b]');`, `["b"]`},
		{"filter expression with predicate", pre + `return ids('(//li)[2]');`, `["b"]`},
		{"grouped position", pre + `return [ids('(//p//span)[last()]').length, str('(//span)[2]')];`, `[1,"2"]`},
		{"operator names as element names", pre + `document.body.insertAdjacentHTML('beforeend', '<and><or>1</or></and>'); return [num('count(//and/or)'), bool('count(//and) and 1')];`, `[1,true]`},
		{"star as multiply", pre + `return [num('2 * 3'), num('count(//li) * 2'), bool('count(//*) > 0')];`, `[6,6,true]`},
		{"text of whole document", pre + `return str('string(/)').indexOf('Three') >= 0;`, `true`},
		{"comparison with node-set and boolean", pre + `return [bool('//li = true()'), bool('//nothing = false()')];`, `[true,true]`},
	})
}
