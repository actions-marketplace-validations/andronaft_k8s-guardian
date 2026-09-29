package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileKeepsModeAndIsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.yaml")
	os.WriteFile(p, []byte("old"), 0o640)
	if err := WriteFile(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	data, _ := os.ReadFile(p)
	if string(data) != "new" || info.Mode().Perm() != 0o640 {
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
	if info, _ := os.Stat(n); info.Mode().Perm() != NewFileMode {
		t.Errorf("new file mode %v", info.Mode().Perm())
	}
}

func TestWriteFileThroughSymlinkKeepsLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "link.yaml")
	os.WriteFile(target, []byte("old"), 0o644)
	os.Symlink("real.yaml", link)
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
