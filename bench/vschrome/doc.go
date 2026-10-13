// Command vschrome is a reproducible, side-by-side benchmark of Sweef's desktop GUI against a full, headed Chromium
// (with its tab strip, toolbar and omnibox), both running under the same Xvfb. Everything is measured from the outside
// (XTEST input, X11 pixel read-back, /proc accounting); nothing inside either browser is instrumented.
//
//	scripts/bench/vschrome.sh            # build, run (hours), write bench/vschrome/results/*.json and docs/vs-chrome.md
//	vschrome run -sweef BIN ...          # the measurement (see scripts/bench/vschrome.sh for the flags)
//	vschrome report -in X.json -out Y.md # regenerate the document from a results file
//	vschrome calib | load | shot | fixture   # debugging aids
//
// docs/vs-chrome.md explains the method and its weaknesses.
package main
