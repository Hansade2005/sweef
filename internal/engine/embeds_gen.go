package engine

// The JavaScript that ships inside the binary is embedded deflated (about a quarter of its size; see internal/zpack
// and docs/development.md, "Binary size budget"). The .js files are the sources you edit; the .js.z files next to
// them are what //go:embed picks up. After editing a .js file run
//
//	go generate ./internal/engine
//
// and commit both. TestEmbeddedJSIsCurrent fails when a .z file is missing or stale.
//
//go:generate go run ../../tools/zpack blob *.js platformjs/*.js
