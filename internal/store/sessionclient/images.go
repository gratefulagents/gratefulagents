package sessionclient

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// maxImageBytes caps the decoded size of a single attached image to guard
// against oversized payloads reaching the model or the database.
const maxImageBytes = 20 * 1024 * 1024 // 20 MiB
const maxImagesPerMessage = 8

// ParseImageDataURL parses a data URL of the form
// "data:<media-type>;base64,<data>" into a MessageImage. It validates the
// base64 payload and enforces a size cap. Non-base64 or non-data URLs are
// rejected.
func ParseImageDataURL(dataURL string) (MessageImage, error) {
	raw := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(raw, "data:") {
		return MessageImage{}, fmt.Errorf("not a data URL")
	}
	raw = strings.TrimPrefix(raw, "data:")
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return MessageImage{}, fmt.Errorf("malformed data URL: missing comma")
	}
	meta := raw[:comma]
	payload := raw[comma+1:]
	if !strings.Contains(meta, "base64") {
		return MessageImage{}, fmt.Errorf("only base64-encoded data URLs are supported")
	}
	mediaType := strings.TrimSpace(strings.Split(meta, ";")[0])
	if mediaType == "" {
		mediaType = "image/png"
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return MessageImage{}, fmt.Errorf("unsupported media type %q", mediaType)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return MessageImage{}, fmt.Errorf("invalid base64 image data: %w", err)
	}
	if len(decoded) == 0 {
		return MessageImage{}, fmt.Errorf("empty image data")
	}
	if len(decoded) > maxImageBytes {
		return MessageImage{}, fmt.Errorf("image exceeds %d byte limit", maxImageBytes)
	}
	return MessageImage{
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(decoded),
	}, nil
}

// ParseImageDataURLs parses a slice of data URLs, returning an error on the
// first malformed entry.
func ParseImageDataURLs(dataURLs []string) ([]MessageImage, error) {
	out := make([]MessageImage, 0, len(dataURLs))
	for _, u := range dataURLs {
		if strings.TrimSpace(u) == "" {
			continue
		}
		img, err := ParseImageDataURL(u)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
		if len(out) > maxImagesPerMessage {
			return nil, fmt.Errorf("too many images: maximum %d per message", maxImagesPerMessage)
		}
	}
	return out, nil
}

// DataURL renders the image back into a "data:<media-type>;base64,<data>" URL.
func (m MessageImage) DataURL() string {
	mediaType := m.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return fmt.Sprintf("data:%s;base64,%s", mediaType, m.Data)
}
