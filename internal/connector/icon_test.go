package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#07c160" d="M0 0h24v24H0z"/></svg>`

func iconPackage(t *testing.T, root, icon string, data []byte) Package {
	t.Helper()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "demo", Name: "Demo", Version: "1.0.0", Type: "cli", AuthMode: AuthDelegated, Icon: icon}
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"connector.json": content, "cli.json": []byte(`{}`)}
	if icon != "" {
		files[icon] = data
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(dir, path), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Package{Manifest: manifest, Dir: dir}
}

func TestConnectorIconDefaultCandidates(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	candidates := []string{"assets/icon.svg", "assets/icon.png", "assets/demo.svg", "assets/demo.png"}
	for first := 0; first <= len(candidates); first++ {
		name := "none"
		if first < len(candidates) {
			name = candidates[first]
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			fixture := iconPackage(t, root, "", nil)
			original, err := os.ReadFile(filepath.Join(fixture.Dir, "connector.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range append([]string{"assets/unrelated.svg"}, candidates[first:]...) {
				data := []byte(testIconSVG)
				if strings.HasSuffix(candidate, ".png") {
					data = encoded.Bytes()
				}
				if err := os.WriteFile(filepath.Join(fixture.Dir, candidate), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			pkg, err := Load(root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			if pkg.Icon != "" || pkg.Version != "1.0.0" {
				t.Fatalf("discovery changed manifest: %+v", pkg.Manifest)
			}
			items, err := (Sources{ExternalRoot: root}).Summaries()
			if err != nil || len(items) != 1 {
				t.Fatalf("summaries: %+v, %v", items, err)
			}
			icon, err := pkg.ReadIcon()
			if first == len(candidates) {
				if !errors.Is(err, os.ErrNotExist) || items[0].Icon != "" || items[0].IconSHA256 != "" {
					t.Fatalf("unexpected default icon: %+v, %v", items[0], err)
				}
			} else {
				mediaType, data := "image/svg+xml", []byte(testIconSVG)
				if strings.HasSuffix(name, ".png") {
					mediaType, data = "image/png", encoded.Bytes()
				}
				if err != nil || icon.Path != name || icon.MediaType != mediaType || !bytes.Equal(icon.Data, data) || icon.SHA256 != digest(data) {
					t.Fatalf("wrong discovered icon: %+v, %v", icon, err)
				}
				if items[0].Icon != name || items[0].IconSHA256 != icon.SHA256 {
					t.Fatalf("missing discovered metadata: %+v", items[0])
				}
			}
			current, err := os.ReadFile(filepath.Join(fixture.Dir, "connector.json"))
			if err != nil || !bytes.Equal(current, original) {
				t.Fatalf("discovery rewrote connector.json: %v", err)
			}
			// Runtime package directory names are content digests, not connector IDs.
			versioned := filepath.Join(root, "content-digest")
			if err := os.Rename(fixture.Dir, versioned); err != nil {
				t.Fatal(err)
			}
			copy, err := LoadDirectory(versioned, "demo")
			if err != nil || copy.iconPath != pkg.iconPath || copy.iconSHA256 != pkg.iconSHA256 {
				t.Fatalf("versioned directory lost connector icon: %v", err)
			}
		})
	}
	t.Run("no assets directory", func(t *testing.T) {
		root := t.TempDir()
		pkg := iconPackage(t, root, "", nil)
		if err := os.Remove(filepath.Join(pkg.Dir, "assets")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "demo"); err != nil {
			t.Fatalf("optional assets directory required: %v", err)
		}
	})
}

func TestConnectorIconExplicitOverridesDefaults(t *testing.T) {
	root := t.TempDir()
	pkg := iconPackage(t, root, "assets/custom.svg", []byte(testIconSVG))
	if err := os.WriteFile(filepath.Join(pkg.Dir, "assets/icon.svg"), []byte(`<svg onload="alert(1)"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root, "demo")
	if err != nil || loaded.iconPath != pkg.Icon {
		t.Fatalf("declared icon did not override defaults: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, "assets/icon.svg"), []byte(testIconSVG), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(pkg.Dir, pkg.Icon)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "demo"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing declared icon fell back to default: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, pkg.Icon), []byte(`<svg onload="alert(1)"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "demo"); err == nil {
		t.Fatal("invalid declared icon fell back to default")
	}
}

func TestConnectorIconDefaultValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"assets/icon.svg", []byte(`<svg onload="alert(1)"/>`)},
		{"assets/icon.svg", nil},
		{"assets/icon.svg", bytes.Repeat([]byte("x"), MaxIconBytes+1)},
		{"assets/icon.png", []byte(testIconSVG)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pkg := iconPackage(t, root, "", nil)
			// An invalid earlier candidate must not fall back to a valid later one.
			for name, data := range map[string][]byte{tc.name: tc.data, "assets/demo.svg": []byte(testIconSVG)} {
				if err := os.WriteFile(filepath.Join(pkg.Dir, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(root, "demo"); err == nil {
				t.Fatal("invalid discovered icon accepted")
			}
		})
	}
}

func TestConnectorIconDiscoveryOnDefinitionSave(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "valid default"
		if !valid {
			name = "invalid default"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			pkg := iconPackage(t, root, "assets/custom.svg", []byte(testIconSVG))
			data := []byte(testIconSVG)
			if !valid {
				data = []byte(`<svg onload="alert(1)"/>`)
			}
			if err := os.WriteFile(filepath.Join(pkg.Dir, "assets/icon.svg"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			previous, err := ReadFile(root, "demo", "connector.json")
			if err != nil {
				t.Fatal(err)
			}
			manifest := pkg.Manifest
			manifest.Icon = ""
			content, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			reloaded := false
			_, err = SaveDefinition(root, File{ID: "demo", File: "connector.json", Content: string(content)}, previous.SHA256, func(candidate Package) error {
				if candidate.Icon != "" || candidate.iconPath != "assets/icon.svg" || candidate.iconSHA256 != digest(data) {
					t.Fatalf("definition validation missed discovered icon: %+v", candidate)
				}
				return nil
			}, func() error { reloaded = true; return nil })
			if (err == nil) != valid || reloaded != valid {
				t.Fatalf("save/reload: valid=%v reloaded=%v err=%v", valid, reloaded, err)
			}
			want := previous.Content
			if valid {
				want = string(content)
			}
			current, err := ReadFile(root, "demo", "connector.json")
			if err != nil || current.Content != want {
				t.Fatalf("save rewrote or replaced manifest unexpectedly: %v", err)
			}
		})
	}
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
	pkg.Icon = ""
	if _, err := pkg.ReadIcon(); err == nil {
		t.Fatal("discovered icon read escaped package after load")
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
