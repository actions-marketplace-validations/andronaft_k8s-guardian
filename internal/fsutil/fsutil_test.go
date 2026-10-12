package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Windows has no Unix permission bits; only the read-only flag maps to Perm.
var unixModes = runtime.GOOS != "windows"

func TestWriteFileKeepsModeAndIsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.yaml")
	os.WriteFile(p, []byte("old"), 0o640)
	if err := WriteFile(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	data, _ := os.ReadFile(p)
	if string(data) != "new" || unixModes && info.Mode().Perm() != 0o640 {
		t.Errorf("got %q mode %v", data, info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
	n := filepath.Join(dir, "new.yaml")
	if err := WriteFile(n, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(n); unixModes && info.Mode().Perm() != NewFileMode {
		t.Errorf("new file mode %v", info.Mode().Perm())
	}
}

func TestWriteFileThroughSymlinkKeepsLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "link.yaml")
	os.WriteFile(target, []byte("old"), 0o644)
	if err := os.Symlink("real.yaml", link); err != nil {
		t.Skipf("symlinks not available: %v", err) // Windows without the privilege
	}
	if err := WriteFile(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Errorf("target not updated: %q", data)
	}
}
