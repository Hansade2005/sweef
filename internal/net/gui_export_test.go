package net

import "testing"

func TestTouchAndQuietAttributes(t *testing.T) {
	d, err := ParseDocument(`<html><body><img id="i" src="/a.png"></body></html>`, "http://x.test/")
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	d.SetMutationListener(func(Mutation) { seen++ })
	n := d.find(d.idOf(findFirst(d.Root, "img")))
	g0 := d.MutationGen()
	d.SetAttrQuiet(n, "data-sweef-lite", "skipped")
	if attr(n, "data-sweef-lite") != "skipped" || seen != 0 || d.MutationGen() != g0 {
		t.Fatalf("quiet set: attr=%q listeners=%d gen %d->%d", attr(n, "data-sweef-lite"), seen, g0, d.MutationGen())
	}
	d.Touch()
	if d.MutationGen() == g0 {
		t.Fatal("Touch must advance the generation")
	}
	d.RemoveAttrQuiet(n, "data-sweef-lite")
	if hasAttr(n, "data-sweef-lite") || seen != 0 {
		t.Fatalf("quiet remove: %v %d", hasAttr(n, "data-sweef-lite"), seen)
	}
}
