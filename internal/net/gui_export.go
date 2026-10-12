package net

// MutationGen returns a counter that increases on every tree or style
// mutation. The GUI compositor compares it between frames to decide whether
// a tab needs repainting (damage tracking) without polling the DOM.
func (d *Doc) MutationGen() uint64 { return d.mutGen.Load() }
