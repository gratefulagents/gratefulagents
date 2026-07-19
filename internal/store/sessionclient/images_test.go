package sessionclient

import "testing"

func TestParseImageDataURL(t *testing.T) {
	// "PNG" base64 of the bytes {1,2,3} is "AQID".
	img, err := ParseImageDataURL("data:image/png;base64,AQID")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", img.MediaType)
	}
	if img.Data != "AQID" {
		t.Errorf("data = %q, want AQID", img.Data)
	}
	if got := img.DataURL(); got != "data:image/png;base64,AQID" {
		t.Errorf("round-trip DataURL = %q", got)
	}
}

func TestParseImageDataURLRejectsNonImage(t *testing.T) {
	if _, err := ParseImageDataURL("data:text/plain;base64,AQID"); err == nil {
		t.Fatal("expected error for non-image media type")
	}
}

func TestParseImageDataURLRejectsNonDataURL(t *testing.T) {
	if _, err := ParseImageDataURL("https://example.com/x.png"); err == nil {
		t.Fatal("expected error for non-data URL")
	}
}

func TestParseImageDataURLRejectsBadBase64(t *testing.T) {
	if _, err := ParseImageDataURL("data:image/png;base64,!!!notbase64"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestParseImageDataURLsSkipsEmpty(t *testing.T) {
	imgs, err := ParseImageDataURLs([]string{"", "data:image/png;base64,AQID", "  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("got %d images, want 1", len(imgs))
	}
}

func TestParseImageDataURLsRejectsTooManyImages(t *testing.T) {
	urls := make([]string, maxImagesPerMessage+1)
	for i := range urls {
		urls[i] = "data:image/png;base64,AQID"
	}
	if _, err := ParseImageDataURLs(urls); err == nil {
		t.Fatal("expected error for too many images")
	}
}

func TestEncodeDecodeImagesMetadata(t *testing.T) {
	images := []MessageImage{{MediaType: "image/png", Data: "AQID", AssetID: "asset-1", AssetVersion: 1, AssetSHA256: "abc", AssetPath: "chat-attachments/run/image.png", ProjectName: "demo"}}
	meta := EncodeUserMessageMetadataWithImages(UserMessageModeImmediate, images)
	got := imagesFromMetadata(meta)
	if len(got) != 1 || got[0].Data != "AQID" || got[0].MediaType != "image/png" {
		t.Fatalf("decoded images = %+v", got)
	}
	if got[0].AssetID != "asset-1" || got[0].AssetVersion != 1 || got[0].AssetSHA256 != "abc" || got[0].AssetPath != "chat-attachments/run/image.png" || got[0].ProjectName != "demo" {
		t.Fatalf("decoded asset reference = %+v", got[0])
	}
	if mode := messageModeFromMetadata(meta); mode != UserMessageModeImmediate {
		t.Errorf("mode = %q, want immediate", mode)
	}
}
