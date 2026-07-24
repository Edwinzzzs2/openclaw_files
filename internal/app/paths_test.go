package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathResolverStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	resolver := newPathResolver(root, "/srv/openclaw/files")

	relative, absolute, err := resolver.resolve("../../etc/passwd")
	if err != nil {
		t.Fatalf("resolve cleaned path: %v", err)
	}
	if relative != "etc/passwd" {
		t.Fatalf("unexpected relative path: %q", relative)
	}
	if !resolver.withinRoot(absolute) {
		t.Fatalf("resolved path escaped root: %q", absolute)
	}
}

func TestPathResolverRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "outside")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	resolver := newPathResolver(root, "/srv/openclaw/files")
	if _, _, err := resolver.resolveExisting("outside"); err == nil {
		t.Fatal("expected symlink to be rejected")
	}
}

func TestCleanFileName(t *testing.T) {
	valid := []string{"video.mp4", "需求说明.pdf", "archive (1).zip"}
	for _, name := range valid {
		if _, err := cleanFileName(name); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}

	invalid := []string{"", ".", "..", "../file", `dir\file`, "bad\x00name"}
	for _, name := range invalid {
		if _, err := cleanFileName(name); err == nil {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
}

func TestPathResolverRejectsMetadataDirectory(t *testing.T) {
	resolver := newPathResolver(t.TempDir(), "/srv/openclaw/files")
	for _, value := range []string{".clawfiles", ".clawfiles/recent.json"} {
		if _, _, err := resolver.resolve(value); err == nil {
			t.Fatalf("metadata path %q accepted", value)
		}
	}
}
