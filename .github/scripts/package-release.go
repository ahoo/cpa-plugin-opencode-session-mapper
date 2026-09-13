package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const pluginID = "opencode-session-mapper"

var archiveEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

func main() {
	libraryPath := flag.String("library", "", "path to the compiled plugin library")
	archivePath := flag.String("archive", "", "path to the output zip archive")
	checksumPath := flag.String("checksum", "", "path to the output checksum file")
	flag.Parse()

	if *libraryPath == "" || *archivePath == "" || *checksumPath == "" {
		fatalf("library, archive, and checksum are required")
	}
	archiveData, errPackage := packageLibrary(*libraryPath, *archivePath)
	if errPackage != nil {
		fatalf("%v", errPackage)
	}
	checksum := sha256.Sum256(archiveData)
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(checksum[:]), filepath.Base(*archivePath))
	if errWrite := os.WriteFile(*checksumPath, []byte(line), 0o644); errWrite != nil {
		fatalf("write checksum: %v", errWrite)
	}
}

func packageLibrary(libraryPath, archivePath string) ([]byte, error) {
	library, errOpen := os.Open(libraryPath)
	if errOpen != nil {
		return nil, fmt.Errorf("open library: %w", errOpen)
	}
	defer library.Close()

	info, errStat := library.Stat()
	if errStat != nil {
		return nil, fmt.Errorf("stat library: %w", errStat)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("library is not a regular file")
	}

	entryName, errName := archiveEntryName(libraryPath)
	if errName != nil {
		return nil, errName
	}

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	header := &zip.FileHeader{
		Name:   entryName,
		Method: zip.Deflate,
	}
	header.SetMode(0o755)
	header.SetModTime(archiveEpoch)
	entry, errEntry := writer.CreateHeader(header)
	if errEntry != nil {
		return nil, fmt.Errorf("create zip entry: %w", errEntry)
	}
	if _, errCopy := io.Copy(entry, library); errCopy != nil {
		return nil, fmt.Errorf("copy library: %w", errCopy)
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, fmt.Errorf("close zip writer: %w", errClose)
	}

	data := archive.Bytes()
	if errWrite := os.WriteFile(archivePath, data, 0o644); errWrite != nil {
		return nil, fmt.Errorf("write archive: %w", errWrite)
	}
	return data, nil
}

func archiveEntryName(libraryPath string) (string, error) {
	extension := filepath.Ext(libraryPath)
	switch extension {
	case ".so", ".dylib", ".dll":
		return pluginID + extension, nil
	default:
		return "", fmt.Errorf("unsupported plugin library extension: %q", extension)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
