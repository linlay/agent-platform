package multimodal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"

	"agent-platform/internal/filetools"
)

const (
	DefaultMaxImageBytes         = filetools.MaxInlineImageBytes
	DefaultReencodeThresholdByte = 400 * 1024
	DefaultJPEGQuality           = 92
)

var (
	ErrUnsupportedImageMime = errors.New("unsupported image mime")
	ErrImageTooLarge        = errors.New("image too large")
	ErrImageIsDirectory     = errors.New("image path is a directory")
)

type ImageLoadOptions struct {
	MaxBytes               int64
	ReencodeThresholdBytes int
	JPEGQuality            int
}

type ImagePayload struct {
	Name       string
	FilePath   string
	MimeType   string
	DataBase64 string
	DataURL    string
	SHA256     string
	SizeBytes  int64
	SentBytes  int
	Reencoded  bool
}

func DefaultImageLoadOptions() ImageLoadOptions {
	return ImageLoadOptions{
		MaxBytes:               DefaultMaxImageBytes,
		ReencodeThresholdBytes: DefaultReencodeThresholdByte,
		JPEGQuality:            DefaultJPEGQuality,
	}
}

func LoadImageFile(path string, mimeHint string, options ImageLoadOptions) (ImagePayload, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ImagePayload{}, fmt.Errorf("image path is required")
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxImageBytes
	}
	if options.JPEGQuality <= 0 {
		options.JPEGQuality = DefaultJPEGQuality
	}
	info, err := os.Stat(path)
	if err != nil {
		return ImagePayload{}, err
	}
	if info.IsDir() {
		return ImagePayload{}, ErrImageIsDirectory
	}
	if info.Size() > options.MaxBytes {
		return ImagePayload{}, fmt.Errorf("%w: %d > %d", ErrImageTooLarge, info.Size(), options.MaxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ImagePayload{}, err
	}
	mime := normalizeImageMime(mimeHint)
	if mime == "" {
		mime = detectImageMime(data)
	}
	if !filetools.IsSupportedImageMime(mime) {
		return ImagePayload{}, ErrUnsupportedImageMime
	}
	outMime := mime
	reencoded := false
	if options.ReencodeThresholdBytes > 0 && len(data) > options.ReencodeThresholdBytes {
		if shrunk, shrunkMime, ok := shrinkImage(data, options.JPEGQuality); ok {
			data = shrunk
			outMime = shrunkMime
			reencoded = true
		}
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	sha := sha256.Sum256(data)
	return ImagePayload{
		FilePath:   path,
		MimeType:   outMime,
		DataBase64: encoded,
		DataURL:    "data:" + outMime + ";base64," + encoded,
		SHA256:     hex.EncodeToString(sha[:]),
		SizeBytes:  info.Size(),
		SentBytes:  len(data),
		Reencoded:  reencoded,
	}, nil
}

func OpenAIImageBlock(image ImagePayload) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": image.DataURL,
		},
	}
}

func detectImageMime(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	return normalizeImageMime(http.DetectContentType(data[:limit]))
}

func normalizeImageMime(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "image/jpg" {
		return "image/jpeg"
	}
	if !filetools.IsSupportedImageMime(mime) {
		return ""
	}
	return mime
}

func shrinkImage(data []byte, quality int) ([]byte, string, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", false
	}
	if buf.Len() >= len(data) {
		return nil, "", false
	}
	return buf.Bytes(), "image/jpeg", true
}

func ReadAllLimited(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultMaxImageBytes
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrImageTooLarge
	}
	return data, nil
}
