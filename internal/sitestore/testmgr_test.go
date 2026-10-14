package sitestore

import "testing"

// Managers made by tests are shut down when the test ends: on Windows an open store file cannot be deleted, so
// t.TempDir's cleanup would otherwise fail ("being used by another process").

func newTestManager(t testing.TB, dir string, budget int64) *Manager {
	t.Helper()
	m := NewManager(dir, budget)
	t.Cleanup(m.Shutdown)
	return m
}

func newTestManagerOptions(t testing.TB, dir string, opts Options) *Manager {
	t.Helper()
	m := NewManagerOptions(dir, opts)
	t.Cleanup(m.Shutdown)
	return m
}
