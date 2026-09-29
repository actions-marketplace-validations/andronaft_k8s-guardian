// Package fsutil writes files safely: atomically (temp file + rename, so a
// crash never leaves a half-written manifest) and keeping the permissions
// of the file being replaced.
package fsutil

import (
	"os"
	"path/filepath"
)

// NewFileMode is used for files that did not exist before.
const NewFileMode os.FileMode = 0o600

// WriteFile atomically replaces path with data. An existing file keeps its
// mode; if path is a symlink the file it points to is replaced (the link
// itself stays). Callers must only pass paths the user named or that were
// found by a symlink-free directory walk.
func WriteFile(path string, data []byte) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	mode := NewFileMode
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".k8s-guardian-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // already failing; the write error is returned
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close() // already failing; the write error is returned
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close() // already failing; the write error is returned
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// MkdirAll creates a directory for generated files (owner and group only).
func MkdirAll(dir string) error { return os.MkdirAll(dir, 0o750) }
