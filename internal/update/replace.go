package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Replacement is an installed-but-not-yet-committed binary swap.
type Replacement struct {
	target, backup string
	done           bool
}

func backupPath(target string) string { return target + ".old" }

// Replace atomically swaps newFile in as target, keeping the previous binary
// as target+".old" until Commit. Steps:
//
//  1. copy newFile next to target (same filesystem) as target+".new", mode copied from target
//  2. rename target -> target+".old"   (works for a running executable on Linux, macOS and Windows)
//  3. rename target+".new" -> target
//
// If step 3 fails the old binary is put back. After the new version has
// started successfully and passed its self-check, call Commit; if it did not,
// call Rollback.
func Replace(target, newFile string) (*Replacement, error) {
	st, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("update: stat target: %w", err)
	}
	staged := target + ".new"
	if err := copyFile(newFile, staged, st.Mode().Perm()|0o100); err != nil {
		os.Remove(staged)
		return nil, fmt.Errorf("update: stage: %w", err)
	}
	r := &Replacement{target: target, backup: backupPath(target)}
	os.Remove(r.backup) // leftover from a previous, committed update
	if err := os.Rename(target, r.backup); err != nil {
		os.Remove(staged)
		return nil, fmt.Errorf("update: move old binary aside: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		rerr := os.Rename(r.backup, target)
		os.Remove(staged)
		if rerr != nil {
			return nil, fmt.Errorf("update: install failed (%v) AND restore failed: %w", err, rerr)
		}
		return nil, fmt.Errorf("update: install new binary: %w", err)
	}
	return r, nil
}

// Rollback restores the previous binary. Safe to call after a failed health check.
func (r *Replacement) Rollback() error {
	if r.done {
		return errors.New("update: replacement already finalised")
	}
	r.done = true
	bad := r.target + ".bad"
	os.Remove(bad)
	if err := os.Rename(r.target, bad); err != nil {
		return fmt.Errorf("update: rollback: %w", err)
	}
	if err := os.Rename(r.backup, r.target); err != nil {
		return fmt.Errorf("update: rollback: %w", err)
	}
	os.Remove(bad) // may fail on Windows if still running; harmless
	return nil
}

// Commit discards the backup. On Windows a running .old file may be locked;
// that is ignored and it is cleaned up by Recover on the next start.
func (r *Replacement) Commit() error {
	if r.done {
		return errors.New("update: replacement already finalised")
	}
	r.done = true
	os.Remove(r.backup)
	return nil
}

// Recover repairs an interrupted update at startup: if target is missing but
// target+".old" exists the swap was cut off between steps 2 and 3 (power loss),
// so restore it. Leftover .new/.bad files are removed.
func Recover(target string) error {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if _, berr := os.Stat(backupPath(target)); berr == nil {
			if err := os.Rename(backupPath(target), target); err != nil {
				return fmt.Errorf("update: recover: %w", err)
			}
		}
	}
	os.Remove(target + ".new")
	os.Remove(target + ".bad")
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// ExecutablePath returns the resolved path of the running binary.
func ExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}
