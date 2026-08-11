package main

import (
	"bytes"
	"testing"
)

func TestPatchWindowsNetOnlyChangesTwoUDPChecks(t *testing.T) {
	source := []byte("before\n" + unsupportedCheck + "\nmiddle\n" + unsupportedCheck + "\nafter\n")
	patched, err := patchWindowsNet(source)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(patched, []byte(compatibilityCheck)) != 2 {
		t.Fatalf("patched source does not contain exactly two compatibility checks:\n%s", patched)
	}
	if bytes.Contains(patched, []byte(unsupportedCheck)) {
		t.Fatal("original checks remain in patched source")
	}
}

func TestPatchWindowsNetRejectsUnexpectedToolchainSource(t *testing.T) {
	if _, err := patchWindowsNet([]byte(unsupportedCheck)); err == nil {
		t.Fatal("patchWindowsNet accepted source with only one matching check")
	}
}
