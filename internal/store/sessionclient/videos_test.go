package sessionclient

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseVideoDataURL(t *testing.T) {
	tests := map[string][]byte{
		"video/mp4":       validMP4(),
		"video/quicktime": validMP4(),
		"video/webm":      validWebM(),
	}
	for mediaType, video := range tests {
		t.Run(mediaType, func(t *testing.T) {
			data, err := parseVideoDataURL(videoDataURL(mediaType, video))
			if err != nil {
				t.Fatalf("parseVideoDataURL() error = %v", err)
			}
			if got := string(data); got != string(video) {
				t.Errorf("data = %q, want raw video bytes", got)
			}
		})
	}
}

func TestParseVideoDataURLRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"https://example.com/video.mp4",
		"data:video/mp4;base64",
		"data:image/png;base64,AQID",
		"data:video/mp4,AQID",
		"data:video/mp4;base64,",
		"data:video/mp4;base64,!!!",
		videoDataURL("video/mp4", []byte("not an mp4")),
		videoDataURL("video/webm", validMP4()),
	}
	for _, dataURL := range tests {
		t.Run(dataURL, func(t *testing.T) {
			_, err := parseVideoDataURL(dataURL)
			if !errors.Is(err, ErrInvalidVideoAttachment) {
				t.Fatalf("parseVideoDataURL() error = %v, want ErrInvalidVideoAttachment", err)
			}
		})
	}
}

func TestParseVideoDataURLRejectsOversizedVideo(t *testing.T) {
	payload := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxVideoBytes+1))
	_, err := parseVideoDataURL("data:video/mp4;base64," + payload)
	if !errors.Is(err, ErrInvalidVideoAttachment) {
		t.Fatalf("parseVideoDataURL() error = %v, want ErrInvalidVideoAttachment", err)
	}
}

func TestParseVideoDataURLsRejectsInvalidMaxFrames(t *testing.T) {
	for _, maxFrames := range []int{0, 9} {
		t.Run(strconv.Itoa(maxFrames), func(t *testing.T) {
			_, err := ParseVideoDataURLs(context.Background(), []string{
				videoDataURL("video/mp4", validMP4()),
			}, maxFrames)
			if !errors.Is(err, ErrInvalidVideoAttachment) {
				t.Fatalf("ParseVideoDataURLs() error = %v, want ErrInvalidVideoAttachment", err)
			}
		})
	}
}

func TestParseVideoDataURLsRejectsMultipleVideos(t *testing.T) {
	_, err := ParseVideoDataURLs(context.Background(), []string{
		videoDataURL("video/mp4", validMP4()),
		videoDataURL("video/mp4", validMP4()),
	}, 1)
	if !errors.Is(err, ErrInvalidVideoAttachment) {
		t.Fatalf("ParseVideoDataURLs() error = %v, want ErrInvalidVideoAttachment", err)
	}
}

func TestParseVideoDataURLsSkipsEmpty(t *testing.T) {
	frames, err := ParseVideoDataURLs(context.Background(), []string{"", "  "}, 0)
	if err != nil {
		t.Fatalf("ParseVideoDataURLs() error = %v", err)
	}
	if frames != nil {
		t.Errorf("frames = %#v, want nil", frames)
	}
}

func TestParseVideoDataURLsRejectsInvalidProbe(t *testing.T) {
	tests := []string{
		`{"streams":[],"format":{"duration":"4"}}`,
		`{"streams":[{"codec_type":"video","width":1920,"height":1080}],"format":{"duration":"0"}}`,
		`{"streams":[{"codec_type":"video","width":4097,"height":1080}],"format":{"duration":"4"}}`,
		`{"streams":[{"codec_type":"video","width":4000,"height":4001}],"format":{"duration":"4"}}`,
		`{"streams":[{"codec_type":"video","width":1920,"height":1080}],"format":{"duration":"121"}}`,
	}
	for _, output := range tests {
		t.Run(output, func(t *testing.T) {
			binDir := t.TempDir()
			writeVideoExecutable(t, binDir, "ffprobe", "#!/bin/sh\nprintf '%s\\n' '"+output+"'\n")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			_, err := ParseVideoDataURLs(context.Background(), []string{videoDataURL("video/mp4", validMP4())}, 1)
			if !errors.Is(err, ErrInvalidVideoAttachment) {
				t.Fatalf("ParseVideoDataURLs() error = %v, want ErrInvalidVideoAttachment", err)
			}
		})
	}
}

func TestParseVideoDataURLsExtractsSortedBoundedFrames(t *testing.T) {
	binDir := t.TempDir()
	argsDir := t.TempDir()
	probeArgsPath := filepath.Join(argsDir, "ffprobe-args")
	ffmpegArgsPath := filepath.Join(argsDir, "ffmpeg-args")
	videoTempDir := t.TempDir()
	writeVideoExecutable(t, binDir, "ffprobe", "#!/bin/sh\n[ \"$LANG\" = C ] && [ -z \"$VIDEO_TEST_SECRET\" ] || exit 2\nprintf '%s\\n' \"$@\" > "+strconv.Quote(probeArgsPath)+"\nprintf '%s\\n' '{\"streams\":[{\"codec_type\":\"video\",\"width\":1920,\"height\":1080}],\"format\":{\"duration\":\"4\"}}'\n")
	writeVideoExecutable(t, binDir, "ffmpeg", "#!/bin/sh\n[ \"$LANG\" = C ] && [ -z \"$VIDEO_TEST_SECRET\" ] || exit 2\nprintf '%s\\n' \"$@\" > "+strconv.Quote(ffmpegArgsPath)+"\nout=\"\"\nfor arg in \"$@\"; do\n\tout=\"$arg\"\ndone\ndir=${out%/*}\nprintf second > \"$dir/frame-000002.jpg\"\nprintf first > \"$dir/frame-000001.jpg\"\nprintf extra > \"$dir/frame-000003.jpg\"\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VIDEO_TEST_SECRET", "must-not-reach-child")
	t.Setenv("VIDEO_TMP_DIR", videoTempDir)

	frames, err := ParseVideoDataURLs(context.Background(), []string{videoDataURL("video/mp4", validMP4())}, 2)
	if err != nil {
		t.Fatalf("ParseVideoDataURLs() error = %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	for i, want := range []string{"first", "second"} {
		if frames[i].MediaType != "image/jpeg" {
			t.Errorf("frame %d media type = %q, want image/jpeg", i, frames[i].MediaType)
		}
		data, err := base64.StdEncoding.DecodeString(frames[i].Data)
		if err != nil {
			t.Fatalf("frame %d has invalid base64: %v", i, err)
		}
		if got := string(data); got != want {
			t.Errorf("frame %d data = %q, want %q", i, got, want)
		}
	}

	for _, argsPath := range []string{probeArgsPath, ffmpegArgsPath} {
		args, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatalf("read command args: %v", err)
		}
		gotArgs := string(args)
		for _, want := range []string{"-f\nmov\n-i\n", "-map\n0:v:0\n"} {
			if strings.Contains(want, "-map") && argsPath == probeArgsPath {
				continue
			}
			if !strings.Contains(gotArgs, want) {
				t.Errorf("command arguments %q do not contain %q", gotArgs, want)
			}
		}
	}

	probeArgs, err := os.ReadFile(probeArgsPath)
	if err != nil {
		t.Fatalf("read ffprobe args: %v", err)
	}
	inputStart := strings.Index(string(probeArgs), "-i\n")
	if inputStart < 0 {
		t.Fatalf("ffprobe arguments %q do not include an input path", probeArgs)
	}
	inputPath := strings.SplitN(string(probeArgs)[inputStart+len("-i\n"):], "\n", 2)[0]
	if got := filepath.Dir(filepath.Dir(inputPath)); got != videoTempDir {
		t.Errorf("video temp parent = %q, want %q", got, videoTempDir)
	}

	args, err := os.ReadFile(ffmpegArgsPath)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	for _, want := range []string{"-frames:v\n2\n", "fps=0.5,scale=1600:1600:force_original_aspect_ratio=decrease\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("ffmpeg arguments %q do not contain %q", args, want)
		}
	}
}

func TestParseVideoDataURLsRejectsTruncatedProbeOutput(t *testing.T) {
	binDir := t.TempDir()
	writeVideoExecutable(t, binDir, "ffprobe", "#!/bin/sh\nprintf '%65537d' 0\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := ParseVideoDataURLs(context.Background(), []string{videoDataURL("video/mp4", validMP4())}, 1)
	if !errors.Is(err, ErrInvalidVideoAttachment) {
		t.Fatalf("ParseVideoDataURLs() error = %v, want ErrInvalidVideoAttachment", err)
	}
}

func TestParseVideoDataURLsHidesCommandStderr(t *testing.T) {
	binDir := t.TempDir()
	writeVideoExecutable(t, binDir, "ffprobe", "#!/bin/sh\nprintf sensitive-child-output >&2\nexit 1\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := ParseVideoDataURLs(context.Background(), []string{videoDataURL("video/mp4", validMP4())}, 1)
	if !errors.Is(err, ErrInvalidVideoAttachment) {
		t.Fatalf("ParseVideoDataURLs() error = %v, want ErrInvalidVideoAttachment", err)
	}
	if strings.Contains(err.Error(), "sensitive-child-output") {
		t.Errorf("error exposes child stderr: %v", err)
	}
}

func TestParseVideoDataURLsPreservesOperationalErrors(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := ParseVideoDataURLs(context.Background(), []string{videoDataURL("video/mp4", validMP4())}, 1)
		if !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("ParseVideoDataURLs() error = %v, want exec.ErrNotFound", err)
		}
		if errors.Is(err, ErrInvalidVideoAttachment) {
			t.Errorf("missing executable was classified as invalid attachment: %v", err)
		}
	})

	t.Run("context timeout", func(t *testing.T) {
		binDir := t.TempDir()
		writeVideoExecutable(t, binDir, "ffprobe", "#!/bin/sh\nsleep 1\n")
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		_, err := ParseVideoDataURLs(ctx, []string{videoDataURL("video/mp4", validMP4())}, 1)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ParseVideoDataURLs() error = %v, want context.DeadlineExceeded", err)
		}
		if errors.Is(err, ErrInvalidVideoAttachment) {
			t.Errorf("timeout was classified as invalid attachment: %v", err)
		}
	})
}

func TestParseVideoDataURLsFailsFastWhenBusy(t *testing.T) {
	videoProcessingSemaphore <- struct{}{}
	videoProcessingSemaphore <- struct{}{}
	defer func() { <-videoProcessingSemaphore }()
	defer func() { <-videoProcessingSemaphore }()

	_, err := ParseVideoDataURLs(context.Background(), []string{
		videoDataURL("video/mp4", validMP4()),
	}, 1)
	if !errors.Is(err, ErrVideoProcessingBusy) {
		t.Fatalf("ParseVideoDataURLs() error = %v, want ErrVideoProcessingBusy", err)
	}
}

func validMP4() []byte {
	return []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
}

func validWebM() []byte {
	return []byte{0x1a, 0x45, 0xdf, 0xa3, 0x93, 0x42, 0x82}
}

func videoDataURL(mediaType string, video []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(video)
}

func writeVideoExecutable(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
