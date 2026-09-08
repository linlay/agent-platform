package connector

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

const testIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#07c160" d="M0 0h24v24H0z"/></svg>`

func iconPackage(t *testing.T, root, icon string, data []byte) Package {
	t.Helper()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "demo", Name: "Demo", Version: "1.0.0", Type: "cli", Icon: icon}
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{"connector.json": content, "cli.json": []byte(`{}`), icon: data} {
		if err := os.WriteFile(filepath.Join(dir, path), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Package{Manifest: manifest, Dir: dir}
}

func TestConnectorIconLoadAndSummary(t *testing.T) {
	root := t.TempDir()
	iconPackage(t, root, "assets/icon.svg", []byte(testIconSVG))
	pkg, err := Load(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	icon, err := pkg.ReadIcon()
	if err != nil || icon.MediaType != "image/svg+xml" || string(icon.Data) != testIconSVG || icon.SHA256 == "" {
		t.Fatalf("invalid icon: %v", err)
	}
	items, err := (Sources{ExternalRoot: root}).Summaries()
	if err != nil || len(items) != 1 || items[0].Icon != "assets/icon.svg" || items[0].IconSHA256 != icon.SHA256 {
		t.Fatalf("missing icon metadata: %v", err)
	}
	// A missing declared image is rejected at load/import, not silently lost.
	if err := os.Remove(filepath.Join(pkg.Dir, pkg.Icon)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "demo"); err == nil {
		t.Fatal("accepted missing declared icon")
	}
}

func TestConnectorIconRejectsEscapesAndActiveSVG(t *testing.T) {
	for _, name := range []string{"/tmp/icon.svg", "assets/../icon.svg", "assets/sub/../../icon.png", "assets\\icon.svg", "icon.svg", "assets/icon.svg#fragment", "assets/icon.html", "assets/icon.svg?x=1", "assets/./icon.svg", "assets/%2e%2e/icon.svg"} {
		if validIconPath(name) {
			t.Fatalf("accepted unsafe icon path %q", name)
		}
	}
	for _, data := range []string{
		`<svg><script>alert(1)</script></svg>`,
		`<svg onload="alert(1)"/>`,
		`<svg><foreignObject><html/></foreignObject></svg>`,
		`<svg><use href="https://example.test/image.svg#icon"/></svg>`,
		`<svg><path fill="url(https://example.test/icon)"/></svg>`,
		`<svg><style>@import 'https://example.test/style';</style></svg>`,
		`<!DOCTYPE svg SYSTEM "file:///private/secret"><svg/>`,
		`<!` + iconSVG11Doctype + ` [<!ENTITY injected SYSTEM "file:///private/secret">]><svg/>`,
		`<!` + iconSVG11Doctype + `><!` + iconSVG11Doctype + `><svg/>`,
		`<svg/><!` + iconSVG11Doctype + `>`,
		`<?xml-stylesheet href="https://example.test/style"?><svg/>`,
		`<svg/><svg/>`, `<html/>`, `<svg>`,
	} {
		if err := validateIconSVG([]byte(data)); err == nil {
			t.Fatalf("accepted unsafe SVG %s", data)
		}
	}
	if err := validateIconSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="a"><stop offset="0" stop-color="#fff"/></linearGradient></defs><path fill="url(#a)" d="M0 0h1v1z"/></svg>`)); err != nil {
		t.Fatal("rejected package-local SVG gradient:", err)
	}
	if err := validateIconSVG([]byte(`<?xml version="1.0" encoding="UTF-8"?><!` + iconSVG11Doctype + `>` + testIconSVG)); err != nil {
		t.Fatal("rejected standard SVG 1.1 document declaration:", err)
	}
	root := t.TempDir()
	pkg := iconPackage(t, root, "assets/icon.svg", []byte(testIconSVG))
	out := filepath.Join(t.TempDir(), "external.svg")
	if err := os.WriteFile(out, []byte(testIconSVG), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(pkg.Dir, pkg.Icon)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(pkg.Dir, pkg.Icon)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := pkg.ReadIcon(); err == nil {
		t.Fatal("icon read escaped package after load")
	}
}

func TestConnectorIconPNGAndSizeValidation(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	pkg := iconPackage(t, t.TempDir(), "assets/icon.png", encoded.Bytes())
	icon, err := pkg.ReadIcon()
	if err != nil || icon.MediaType != "image/png" {
		t.Fatalf("valid PNG failed: %v", err)
	}
	for _, data := range [][]byte{[]byte(testIconSVG), encoded.Bytes()[:len(encoded.Bytes())-8], bytes.Repeat([]byte("x"), MaxIconBytes+1)} {
		if err := os.WriteFile(filepath.Join(pkg.Dir, pkg.Icon), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := pkg.ReadIcon(); err == nil {
			t.Fatal("accepted corrupt, oversized or wrong-type PNG")
		}
	}
}
