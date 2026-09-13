package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageLibraryIsDeterministicAndSingleRooted(t *testing.T) {
	dir := t.TempDir()
	libraryPath := filepath.Join(dir, "opencode-session-mapper-v0.3.1.so")
	content := []byte("shared-library-fixture")
	if err := os.WriteFile(libraryPath, content, 0o755); err != nil {
		t.Fatal(err)
	}

	firstPath := filepath.Join(dir, "first.zip")
	secondPath := filepath.Join(dir, "second.zip")
	first, err := packageLibrary(libraryPath, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := packageLibrary(libraryPath, secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("archives differ for identical input")
	}

	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(reader.File))
	}
	entry := reader.File[0]
	if entry.Name != "opencode-session-mapper.so" {
		t.Fatalf("entry name = %q", entry.Name)
	}
	if entry.Mode().Perm() != 0o755 {
		t.Fatalf("entry mode = %o, want 755", entry.Mode().Perm())
	}
	if !entry.Modified.Equal(archiveEpoch) {
		t.Fatalf("entry timestamp = %s, want %s", entry.Modified, archiveEpoch)
	}
	stream, err := entry.Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("entry content = %q", got)
	}
}

func TestArchiveEntryNameRejectsUnsupportedExtension(t *testing.T) {
	if _, err := archiveEntryName("opencode-session-mapper.a"); err == nil {
		t.Fatal("unsupported library extension was accepted")
	}
}
