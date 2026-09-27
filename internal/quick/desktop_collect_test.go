package quick

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopCollectionExportsOnlyKnownArtifactsAndMarksPartial(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"manifest.json", "status.ndjson", "INCOMPLETE", "private.conf"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := encodeDesktopCollection(directory, errors.New("runtime changed during collection"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Complete bool
		Detail   string
		Archive  []byte
	}
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Detail == "" {
		t.Fatal("partial capture reported complete")
	}
	archive, err := zip.NewReader(bytes.NewReader(result.Archive), int64(len(result.Archive)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 3 {
		t.Fatalf("unexpected archive files: %+v", archive.File)
	}
	for _, file := range archive.File {
		if file.Name == "private.conf" {
			t.Fatal("configuration included in diagnostics")
		}
	}
}

func TestDesktopCollectionEnforcesArchiveBudget(t *testing.T) {
	directory := t.TempDir()
	data := make([]byte, desktopArchiveLimit+4096)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "status.ndjson"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := encodeDesktopCollection(directory, nil); err == nil {
		t.Fatal("oversized archive accepted")
	}
}

func TestDesktopCollectionRejectsLinkedArtifacts(t *testing.T) {
	directory := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("must not export"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(directory, "status.ndjson")); err != nil {
		t.Skip(err)
	}
	if _, err := encodeDesktopCollection(directory, nil); err == nil {
		t.Fatal("symbolic link exported")
	}
}
