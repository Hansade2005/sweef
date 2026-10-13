//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package sitestore

import "errors"

// diskFree is unknown on this platform: the budget then falls back to Options.BudgetCap.
func diskFree(dir string) (uint64, error) {
	return 0, errors.New("sitestore: free space is not available on this platform")
}
