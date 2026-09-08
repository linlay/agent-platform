package connector

import (
	"bytes"
	"encoding/xml"
	"errors"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxIconBytes = 256 << 10

type IconAsset struct {
	Data      []byte
	MediaType string
	SHA256    string
}

func validIconPath(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return strings.HasPrefix(name, "assets/") && path.Clean(name) == name &&
		!strings.ContainsAny(name, "\\:%?#\x00\r\n") && (ext == ".svg" || ext == ".png")
}

// ReadIcon serves only the manifest's declared image, never arbitrary assets or
// authentication state. OpenRoot constrains symlinks even after catalog loading.
func (p Package) ReadIcon() (IconAsset, error) {
	if p.Icon == "" {
		return IconAsset{}, os.ErrNotExist
	}
	if !validIconPath(p.Icon) {
		return IconAsset{}, errors.New("invalid connector icon path")
	}
	root, err := os.OpenRoot(p.Dir)
	if err != nil {
		return IconAsset{}, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(p.Icon))
	if err != nil {
		return IconAsset{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return IconAsset{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxIconBytes {
		return IconAsset{}, errors.New("connector icon must be a regular image of at most 256 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxIconBytes+1))
	if err != nil {
		return IconAsset{}, err
	}
	if len(data) == 0 || len(data) > MaxIconBytes {
		return IconAsset{}, errors.New("invalid connector icon size")
	}
	mediaType := "image/svg+xml"
	if strings.EqualFold(path.Ext(p.Icon), ".png") {
		mediaType = "image/png"
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
			return IconAsset{}, errors.New("connector icon must be a valid PNG of at most 4096 by 4096 pixels")
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			return IconAsset{}, errors.New("connector icon contains invalid PNG data")
		}
	} else if err := validateIconSVG(data); err != nil {
		return IconAsset{}, err
	}
	return IconAsset{Data: data, MediaType: mediaType, SHA256: digest(data)}, nil
}

// Brand icons need only static vector shapes. Reject scripting, embedded pages,
// animations and remote resources instead of serving arbitrary SVG documents.
func validateIconSVG(data []byte) error {
	allowed := map[string]bool{}
	for _, tag := range []string{"svg", "g", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "defs", "linearGradient", "radialGradient", "stop", "clipPath", "mask", "use", "title", "desc"} {
		allowed[tag] = true
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	hasDoctype := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("connector icon must be valid SVG XML")
		}
		switch item := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || item.Name.Local != "svg" {
					return errors.New("connector icon must have one SVG root")
				}
			}
			depth++
			if depth > 64 || !allowed[item.Name.Local] || item.Name.Space != "" && item.Name.Space != "http://www.w3.org/2000/svg" {
				return errors.New("connector icon contains unsupported SVG elements")
			}
			for _, attr := range item.Attr {
				name, value := strings.ToLower(attr.Name.Local), strings.TrimSpace(attr.Value)
				if strings.HasPrefix(name, "on") || name == "base" || name == "href" && !strings.HasPrefix(value, "#") {
					return errors.New("connector icon contains active or external SVG attributes")
				}
				lower := strings.ToLower(value)
				if strings.ContainsAny(value, "\\") || strings.Contains(lower, "@import") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "expression(") {
					return errors.New("connector icon contains unsupported SVG styling")
				}
				for _, match := range iconSVGURL.FindAllStringSubmatch(value, -1) {
					ref := strings.Trim(strings.TrimSpace(match[1]), "\"'")
					if !strings.HasPrefix(ref, "#") {
						return errors.New("connector icon cannot reference external images or styles")
					}
				}
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			// Common SVG 1.1 exporters include this fixed declaration. Go's XML
			// decoder does not fetch its DTD; custom DTDs/entities stay forbidden.
			if roots != 0 || hasDoctype || strings.Join(strings.Fields(string(item)), " ") != iconSVG11Doctype {
				return errors.New("connector icon cannot contain custom XML directives")
			}
			hasDoctype = true
		case xml.ProcInst:
			if item.Target != "xml" || roots != 0 {
				return errors.New("connector icon cannot contain XML processing instructions")
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(item)) != "" {
				return errors.New("connector icon has content outside its SVG root")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return errors.New("connector icon must contain a complete SVG")
	}
	return nil
}

const iconSVG11Doctype = `DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd"`

var iconSVGURL = regexp.MustCompile(`(?i)url\s*\(([^)]*)\)`)
