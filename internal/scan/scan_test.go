package scan

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(files []File) []string {
	var found []string
	for _, file := range files {
		found = append(found, file.Path)
	}
	sort.Strings(found)
	return found
}

func TestOnlyVideoIsListed(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Alfred J. Kwak", "S01E01.mkv"))
	write(t, filepath.Join(root, "Alfred J. Kwak", "S01E01.srt"))
	write(t, filepath.Join(root, "Alfred J. Kwak", "poster.jpg"))
	write(t, filepath.Join(root, "Flodder (1986).MP4"))
	write(t, filepath.Join(root, "taxes.pdf"))

	found, err := Folder("folder", root)
	if err != nil {
		t.Fatal(err)
	}
	got := paths(found)
	want := []string{"Alfred J. Kwak/S01E01.mkv", "Flodder (1986).MP4"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("listed %v, want %v", got, want)
	}
}

func TestHiddenThingsStayHidden(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".private", "home video.mkv"))
	write(t, filepath.Join(root, "Series", ".partial.mkv"))
	write(t, filepath.Join(root, "Series", "S01E01.mkv"))

	found, _ := Folder("folder", root)
	if got := paths(found); len(got) != 1 || got[0] != "Series/S01E01.mkv" {
		t.Fatalf("listed %v", got)
	}
}

func TestEmptyFilesAreNotFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "S01E01.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if found, _ := Folder("folder", root); len(found) != 0 {
		t.Fatalf("listed %v", paths(found))
	}
}

// A link inside a shared folder that leads out of it must not share what it leads to.
func TestALinkOutOfTheFolderIsNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows")
	}
	outside := t.TempDir()
	write(t, filepath.Join(outside, "home video.mkv"))
	root := t.TempDir()
	write(t, filepath.Join(root, "S01E01.mkv"))
	if err := os.Symlink(filepath.Join(outside, "home video.mkv"), filepath.Join(root, "S01E02.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "More")); err != nil {
		t.Fatal(err)
	}

	found, _ := Folder("folder", root)
	if got := paths(found); len(got) != 1 || got[0] != "S01E01.mkv" {
		t.Fatalf("listed %v", got)
	}
}

func TestALinkThatStaysInsideIsFine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "real", "S01E01.mkv"))
	if err := os.Symlink(filepath.Join(root, "real", "S01E01.mkv"), filepath.Join(root, "pilot.mkv")); err != nil {
		t.Fatal(err)
	}
	if found, _ := Folder("folder", root); len(found) != 2 {
		t.Fatalf("listed %v", paths(found))
	}
}

func TestAnIdStaysTheSameAndIsOnlyGoodForWhatWasFound(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "S01E01.mkv"))
	first, _ := Folder("folder", root)
	second, _ := Folder("folder", root)
	if first[0].ID != second[0].ID {
		t.Fatal("the same file got two ids")
	}

	index := NewIndex()
	index.Replace("folder", first)
	if _, ok := index.Get(first[0].ID); !ok {
		t.Fatal("a listed file was not found")
	}
	// Whatever the archive sends, a path is never a way in: only an id made here is.
	for _, guess := range []string{"", "../../etc/passwd", "/etc/passwd", `C:\Windows\win.ini`, first[0].Abs} {
		if _, ok := index.Get(guess); ok {
			t.Fatalf("%q was served, and it is not an id", guess)
		}
	}
	index.Drop("folder")
	if _, ok := index.Get(first[0].ID); ok {
		t.Fatal("a file of a folder that is no longer shared was still there")
	}
}
