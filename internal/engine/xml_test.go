package engine

import "testing"

func TestDOMParserXML(t *testing.T) {
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	evalJSON(t, tab, `(function () {
	  var o = {};
	  var mpd = '<?xml version="1.0" encoding="UTF-8"?>\n<!-- c --><MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:cenc="urn:mpeg:cenc:2013" type="static" mediaPresentationDuration="PT4S">' +
	    '<BaseURL>http://x/a&amp;b/</BaseURL><Period id="p"><AdaptationSet mimeType="video/mp4" cenc:default="1"><Representation id="1" bandwidth="100"/><Representation id="2" bandwidth="200"/></AdaptationSet></Period>' +
	    '<![CDATA[raw <b>]]></MPD>';
	  var d = new DOMParser().parseFromString(mpd, 'application/xml');
	  var root = d.documentElement;
	  o.root = [root.tagName, root.localName, root.namespaceURI, d.nodeType, d.contentType, root.nodeType, root.getAttribute('type'), root.attributes.length];
	  o.noParserError = d.getElementsByTagName('parsererror').length;
	  var reps = root.getElementsByTagName('Representation');
	  o.reps = [reps.length, reps[1].getAttribute('bandwidth'), reps[0].parentNode.tagName, reps[0].nextElementSibling.getAttribute('id'), reps[1].nextElementSibling];
	  o.ns = [root.getElementsByTagNameNS('urn:mpeg:dash:schema:mpd:2011', 'Period').length, root.getElementsByTagNameNS('*', 'Representation').length, root.getElementsByTagName('representation').length];
	  var as = root.getElementsByTagName('AdaptationSet')[0];
	  o.attrNS = [as.getAttributeNS('urn:mpeg:cenc:2013', 'default'), as.getAttribute('cenc:default'), as.hasAttribute('mimeType'), as.attributes.getNamedItem('mimeType').value];
	  o.text = [root.getElementsByTagName('BaseURL')[0].textContent, root.textContent.indexOf('raw <b>') >= 0];
	  o.kids = [root.children.length, root.childNodes.length, root.firstChild.nodeName, root.lastChild.nodeType, root.firstElementChild.tagName];
	  o.qs = [d.querySelectorAll('AdaptationSet > Representation').length, root.querySelector('Period Representation').getAttribute('id'), d.querySelector('Nope')];
	  o.ser = new XMLSerializer().serializeToString(reps[0]);
	  var bad = new DOMParser().parseFromString('<a><b></a>', 'text/xml');
	  o.bad = [bad.getElementsByTagName('parsererror').length, bad.documentElement.tagName];
	  o.badNS = new DOMParser().parseFromString('<p:a/>', 'text/xml').getElementsByTagName('parsererror').length;
	  o.entity = new DOMParser().parseFromString('<a t="&lt;&#65;&#x42;">&quot;x&apos;</a>', 'text/xml').documentElement;
	  o.entity = [o.entity.getAttribute('t'), o.entity.textContent];
	  o.empty = new DOMParser().parseFromString('', 'text/xml').getElementsByTagName('parsererror').length;
	  o.html = new DOMParser().parseFromString('<p>hi</p>', 'text/html').body.firstChild.tagName;
	  o.svg = new DOMParser().parseFromString('<svg xmlns="http://www.w3.org/2000/svg"><path/></svg>', 'image/svg+xml').querySelector('path') !== null;
	  return o;
	})()`, &r)
	eq := func(name string, got any, want ...any) {
		t.Helper()
		g, _ := got.([]any)
		if len(g) != len(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
		for i := range want {
			if g[i] != want[i] {
				t.Errorf("%s[%d]: got %v, want %v (%v)", name, i, g[i], want[i], got)
			}
		}
	}
	eq("root", r["root"], "MPD", "MPD", "urn:mpeg:dash:schema:mpd:2011", 9.0, "application/xml", 1.0, "static", 4.0)
	if r["noParserError"] != 0.0 {
		t.Errorf("parsererror in a valid document: %v", r["noParserError"])
	}
	eq("reps", r["reps"], 2.0, "200", "AdaptationSet", "2", nil)
	eq("ns", r["ns"], 1.0, 2.0, 0.0)
	eq("attrNS", r["attrNS"], "1", "1", true, "video/mp4")
	eq("text", r["text"], "http://x/a&b/", true)
	eq("kids", r["kids"], 2.0, 3.0, "BaseURL", 4.0, "BaseURL")
	eq("qs", r["qs"], 2.0, "1", nil)
	if r["ser"] != `<Representation id="1" bandwidth="100"/>` {
		t.Errorf("serialized: %v", r["ser"])
	}
	eq("bad", r["bad"], 1.0, "parsererror")
	if r["badNS"] != 1.0 || r["empty"] != 1.0 {
		t.Errorf("undefined prefix / empty document must give a parsererror: %v %v", r["badNS"], r["empty"])
	}
	eq("entity", r["entity"], "<AB", `"x'`)
	if r["html"] != "P" || r["svg"] != true {
		t.Errorf("html / svg parse: %v %v", r["html"], r["svg"])
	}
}
