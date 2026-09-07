package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestInstalledBuildRejectsTampering(t *testing.T) {
	dir := t.TempDir()
	body := []byte("tested executable")
	sum := sha256.Sum256(body)
	record := buildRecord{"v7.2.152-quotiofix.0123456789ab", "0123456789abcdef0123456789abcdef01234567", hex.EncodeToString(sum[:])}
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "BUILD.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLIProxyAPI"), body, 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyBuild(dir, record.Version, record.Commit); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLIProxyAPI"), []byte("unverified replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyBuild(dir, record.Version, record.Commit); err == nil {
		t.Fatal("accepted unverified replacement engine")
	}
}

func TestActivationFailurePreservesExistingSelection(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "current")
	if err := os.Symlink("known-good", current); err != nil {
		t.Fatal(err)
	}
	// A conflicting promotion entry must not damage the current selection.
	conflict := current + ".promote-" + strconv.Itoa(os.Getpid())
	if err := os.Mkdir(conflict, 0700); err != nil {
		t.Fatal(err)
	}
	if err := activate(current, "candidate"); err == nil {
		t.Fatal("expected promotion to fail")
	}
	selected, err := os.Readlink(current)
	if err != nil || selected != "known-good" {
		t.Fatalf("working selection changed: %q %v", selected, err)
	}
}
