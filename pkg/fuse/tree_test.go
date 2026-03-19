package ghfuse

import (
	"testing"

	"github.com/berthadev/ghfs/pkg/store"
)

func TestBuildTree(t *testing.T) {
	entries := []store.FileEntry{
		{Path: "/a.txt", Size: 10, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/photos/img.jpg", Size: 200, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/photos/vacation/beach.jpg", Size: 300, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/docs/readme.txt", Size: 50, CreatedAt: "2024-01-01T00:00:00Z"},
	}

	root := BuildTree(entries)

	// Root should have Children: photos, docs and Files: a.txt
	if len(root.Children) != 2 {
		t.Fatalf("root children: got %d, want 2", len(root.Children))
	}
	if _, ok := root.Children["photos"]; !ok {
		t.Fatal("root missing child 'photos'")
	}
	if _, ok := root.Children["docs"]; !ok {
		t.Fatal("root missing child 'docs'")
	}
	if len(root.Files) != 1 {
		t.Fatalf("root files: got %d, want 1", len(root.Files))
	}
	if vp, ok := root.Files["a.txt"]; !ok || vp != "a.txt" {
		t.Fatalf("root file 'a.txt': got %q, %v", vp, ok)
	}

	// photos should have Children: vacation and Files: img.jpg
	photos := root.Children["photos"]
	if len(photos.Children) != 1 {
		t.Fatalf("photos children: got %d, want 1", len(photos.Children))
	}
	if _, ok := photos.Children["vacation"]; !ok {
		t.Fatal("photos missing child 'vacation'")
	}
	if len(photos.Files) != 1 {
		t.Fatalf("photos files: got %d, want 1", len(photos.Files))
	}
	if vp, ok := photos.Files["img.jpg"]; !ok || vp != "photos/img.jpg" {
		t.Fatalf("photos file 'img.jpg': got %q, %v", vp, ok)
	}

	// vacation should have Files: beach.jpg
	vacation := photos.Children["vacation"]
	if len(vacation.Children) != 0 {
		t.Fatalf("vacation children: got %d, want 0", len(vacation.Children))
	}
	if vp, ok := vacation.Files["beach.jpg"]; !ok || vp != "photos/vacation/beach.jpg" {
		t.Fatalf("vacation file 'beach.jpg': got %q, %v", vp, ok)
	}

	// docs should have Files: readme.txt
	docs := root.Children["docs"]
	if len(docs.Files) != 1 {
		t.Fatalf("docs files: got %d, want 1", len(docs.Files))
	}
	if vp, ok := docs.Files["readme.txt"]; !ok || vp != "docs/readme.txt" {
		t.Fatalf("docs file 'readme.txt': got %q, %v", vp, ok)
	}
}

func TestLookup(t *testing.T) {
	entries := []store.FileEntry{
		{Path: "/photos/vacation/beach.jpg", Size: 300, CreatedAt: "2024-01-01T00:00:00Z"},
	}
	root := BuildTree(entries)

	// Valid lookup.
	node, ok := root.Lookup([]string{"photos", "vacation"})
	if !ok {
		t.Fatal("Lookup photos/vacation: expected true")
	}
	if _, exists := node.Files["beach.jpg"]; !exists {
		t.Fatal("Lookup photos/vacation: missing beach.jpg")
	}

	// Valid lookup for intermediate dir.
	node, ok = root.Lookup([]string{"photos"})
	if !ok {
		t.Fatal("Lookup photos: expected true")
	}
	if _, exists := node.Children["vacation"]; !exists {
		t.Fatal("Lookup photos: missing child vacation")
	}

	// Invalid lookup.
	_, ok = root.Lookup([]string{"nonexistent"})
	if ok {
		t.Fatal("Lookup nonexistent: expected false")
	}

	// Empty parts returns root.
	node, ok = root.Lookup([]string{})
	if !ok || node != root {
		t.Fatal("Lookup empty: expected root")
	}
}

func TestLookupFile(t *testing.T) {
	entries := []store.FileEntry{
		{Path: "/photos/img.jpg", Size: 200, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/a.txt", Size: 10, CreatedAt: "2024-01-01T00:00:00Z"},
	}
	root := BuildTree(entries)

	// Find nested file.
	vp, ok := root.LookupFile([]string{"photos", "img.jpg"})
	if !ok {
		t.Fatal("LookupFile photos/img.jpg: expected true")
	}
	if vp != "photos/img.jpg" {
		t.Fatalf("LookupFile photos/img.jpg: got %q", vp)
	}

	// Find root-level file.
	vp, ok = root.LookupFile([]string{"a.txt"})
	if !ok {
		t.Fatal("LookupFile a.txt: expected true")
	}
	if vp != "a.txt" {
		t.Fatalf("LookupFile a.txt: got %q", vp)
	}

	// File not found.
	_, ok = root.LookupFile([]string{"photos", "missing.jpg"})
	if ok {
		t.Fatal("LookupFile missing: expected false")
	}

	// Empty parts.
	_, ok = root.LookupFile([]string{})
	if ok {
		t.Fatal("LookupFile empty: expected false")
	}
}

func TestAddFile(t *testing.T) {
	root := NewDirTree()

	root.AddFile("/music/rock/song.mp3")

	// Intermediate dirs should be created.
	node, ok := root.Lookup([]string{"music", "rock"})
	if !ok {
		t.Fatal("AddFile: music/rock dir not created")
	}
	if vp, exists := node.Files["song.mp3"]; !exists || vp != "music/rock/song.mp3" {
		t.Fatalf("AddFile: song.mp3 not found or wrong path: %q, %v", vp, exists)
	}

	// Add a root-level file.
	root.AddFile("/readme.md")
	if vp, exists := root.Files["readme.md"]; !exists || vp != "readme.md" {
		t.Fatalf("AddFile root: readme.md not found or wrong path: %q, %v", vp, exists)
	}
}

func TestRemoveFile(t *testing.T) {
	root := NewDirTree()
	root.AddFile("/a/b/c/file.txt")
	root.AddFile("/a/b/other.txt")

	// Remove deep file; c/ should be cleaned up but b/ stays (has other.txt).
	root.RemoveFile("/a/b/c/file.txt")

	_, ok := root.Lookup([]string{"a", "b", "c"})
	if ok {
		t.Fatal("RemoveFile: dir c should be removed")
	}
	node, ok := root.Lookup([]string{"a", "b"})
	if !ok {
		t.Fatal("RemoveFile: dir a/b should still exist")
	}
	if _, exists := node.Files["other.txt"]; !exists {
		t.Fatal("RemoveFile: other.txt should still exist")
	}

	// Remove the remaining file; entire tree should clean up.
	root.RemoveFile("/a/b/other.txt")
	if len(root.Children) != 0 {
		t.Fatalf("RemoveFile: root should have no children, got %d", len(root.Children))
	}

	// Removing a non-existent file should be a no-op.
	root.RemoveFile("/nonexistent/file.txt")
}

func TestReadDir(t *testing.T) {
	entries := []store.FileEntry{
		{Path: "/a.txt", Size: 10, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/z.txt", Size: 20, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/photos/img.jpg", Size: 200, CreatedAt: "2024-01-01T00:00:00Z"},
		{Path: "/docs/readme.txt", Size: 50, CreatedAt: "2024-01-01T00:00:00Z"},
	}
	root := BuildTree(entries)

	dirs, files := root.ReadDir()

	// Dirs should be sorted.
	if len(dirs) != 2 || dirs[0] != "docs" || dirs[1] != "photos" {
		t.Fatalf("ReadDir dirs: got %v, want [docs photos]", dirs)
	}

	// Files should be sorted.
	if len(files) != 2 || files[0] != "a.txt" || files[1] != "z.txt" {
		t.Fatalf("ReadDir files: got %v, want [a.txt z.txt]", files)
	}
}
