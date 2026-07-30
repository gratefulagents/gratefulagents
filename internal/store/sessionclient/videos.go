package sessionclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxVideoBytes      = 20 * 1024 * 1024
	maxVideoDuration   = 2 * time.Minute
	maxVideoDimension  = 4096
	maxVideoPixels     = 16_000_000
	maxFrameBytes      = 5 * 1024 * 1024
	maxFrameTotalBytes = 20 * 1024 * 1024
	maxVideoFrames     = 8
	ffprobeTimeout     = 5 * time.Second
	ffmpegTimeout      = 20 * time.Second
	maxProbeOutput     = 64 * 1024
	maxStderrOutput    = 16 * 1024
)

var (
	ErrInvalidVideoAttachment = errors.New("invalid video attachment")
	ErrVideoProcessingBusy    = errors.New("video processing capacity reached")
	videoProcessingSemaphore  = make(chan struct{}, 2)
)

var supportedVideoMediaTypes = map[string]bool{
	"video/mp4":       true,
	"video/quicktime": true,
	"video/webm":      true,
}

type videoProbe struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// ParseVideoDataURLs extracts JPEG frames from one video data URL.
func ParseVideoDataURLs(ctx context.Context, dataURLs []string, maxFrames int) ([]MessageImage, error) {
	var dataURL string
	for _, value := range dataURLs {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if dataURL != "" {
			return nil, fmt.Errorf("%w: too many videos: maximum 1 per message", ErrInvalidVideoAttachment)
		}
		dataURL = value
	}
	if dataURL == "" {
		return nil, nil
	}
	if maxFrames < 1 || maxFrames > maxVideoFrames {
		return nil, fmt.Errorf("%w: max frames must be between 1 and %d", ErrInvalidVideoAttachment, maxVideoFrames)
	}

	// Admit work before allocating the decoded payload. Requests beyond the two
	// active decoder slots fail fast instead of retaining large queued videos.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case videoProcessingSemaphore <- struct{}{}:
		defer func() { <-videoProcessingSemaphore }()
	default:
		return nil, ErrVideoProcessingBusy
	}

	video, err := parseVideoDataURL(dataURL)
	if err != nil {
		return nil, err
	}

	tempParent := os.Getenv("VIDEO_TMP_DIR")
	if tempParent == "" {
		tempParent = os.TempDir()
	}
	dir, err := os.MkdirTemp(tempParent, "sessionclient-video-*")
	if err != nil {
		return nil, fmt.Errorf("create video temp directory: %w", err)
	}
	defer os.RemoveAll(dir)

	inputPath := filepath.Join(dir, "input")
	if err := os.WriteFile(inputPath, video, 0o600); err != nil {
		return nil, fmt.Errorf("write video input: %w", err)
	}

	demuxer := videoDemuxer(video)
	probeCtx, cancelProbe := context.WithTimeout(ctx, ffprobeTimeout)
	probeOutput := &limitedBuffer{limit: maxProbeOutput}
	probeErr := runVideoCommand(probeCtx, "ffprobe", probeOutput, "-v", "error", "-protocol_whitelist", "file,pipe", "-f", demuxer, "-i", inputPath, "-show_entries", "stream=codec_type,width,height:format=duration", "-of", "json")
	probeContextErr := probeCtx.Err()
	cancelProbe()
	if probeErr != nil {
		if probeContextErr != nil {
			return nil, probeContextErr
		}
		if errors.Is(probeErr, exec.ErrNotFound) {
			return nil, fmt.Errorf("run ffprobe: %w", probeErr)
		}
		return nil, fmt.Errorf("%w: ffprobe failed", ErrInvalidVideoAttachment)
	}
	if probeOutput.truncated {
		return nil, fmt.Errorf("%w: ffprobe output exceeds %d byte limit", ErrInvalidVideoAttachment, maxProbeOutput)
	}

	var probe videoProbe
	if err := json.Unmarshal(probeOutput.Bytes(), &probe); err != nil {
		return nil, fmt.Errorf("%w: decode ffprobe output: %v", ErrInvalidVideoAttachment, err)
	}
	var stream *struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	}
	for i := range probe.Streams {
		if probe.Streams[i].CodecType == "video" {
			stream = &probe.Streams[i]
			break
		}
	}
	if stream == nil {
		return nil, fmt.Errorf("%w: video has no video stream", ErrInvalidVideoAttachment)
	}
	if stream.Width <= 0 || stream.Height <= 0 || stream.Width > maxVideoDimension || stream.Height > maxVideoDimension || int64(stream.Width)*int64(stream.Height) > maxVideoPixels {
		return nil, fmt.Errorf("%w: invalid video dimensions %dx%d", ErrInvalidVideoAttachment, stream.Width, stream.Height)
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > maxVideoDuration.Seconds() {
		return nil, fmt.Errorf("%w: invalid video duration %q", ErrInvalidVideoAttachment, probe.Format.Duration)
	}

	outputPattern := filepath.Join(dir, "frame-%06d.jpg")
	filter := fmt.Sprintf("fps=%g,scale=1600:1600:force_original_aspect_ratio=decrease", float64(maxFrames)/duration)
	ffmpegCtx, cancelFFmpeg := context.WithTimeout(ctx, ffmpegTimeout)
	ffmpegOutput := &limitedBuffer{limit: maxProbeOutput}
	ffmpegErr := runVideoCommand(ffmpegCtx, "ffmpeg", ffmpegOutput, "-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe", "-threads", "1", "-f", demuxer, "-i", inputPath, "-map", "0:v:0", "-filter_threads", "1", "-vf", filter, "-q:v", "4", "-frames:v", strconv.Itoa(maxFrames), outputPattern)
	ffmpegContextErr := ffmpegCtx.Err()
	cancelFFmpeg()
	if ffmpegErr != nil {
		if ffmpegContextErr != nil {
			return nil, ffmpegContextErr
		}
		if errors.Is(ffmpegErr, exec.ErrNotFound) {
			return nil, fmt.Errorf("run ffmpeg: %w", ffmpegErr)
		}
		return nil, fmt.Errorf("%w: ffmpeg frame extraction failed", ErrInvalidVideoAttachment)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read video frames: %w", err)
	}
	framePaths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			framePaths = append(framePaths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(framePaths)

	frames := make([]MessageImage, 0, min(len(framePaths), maxFrames))
	var totalFrameBytes int64
	for _, path := range framePaths {
		if len(frames) == maxFrames {
			break
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat video frame: %w", err)
		}
		if info.Size() == 0 {
			return nil, fmt.Errorf("%w: empty video frame", ErrInvalidVideoAttachment)
		}
		if info.Size() > maxFrameBytes {
			return nil, fmt.Errorf("%w: video frame exceeds %d byte limit", ErrInvalidVideoAttachment, maxFrameBytes)
		}
		totalFrameBytes += info.Size()
		if totalFrameBytes > maxFrameTotalBytes {
			return nil, fmt.Errorf("%w: video frames exceed %d byte total limit", ErrInvalidVideoAttachment, maxFrameTotalBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read video frame: %w", err)
		}
		frames = append(frames, MessageImage{
			MediaType: "image/jpeg",
			Data:      base64.StdEncoding.EncodeToString(data),
		})
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("%w: ffmpeg produced no video frames", ErrInvalidVideoAttachment)
	}
	return frames, nil
}

func runVideoCommand(ctx context.Context, name string, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LANG=C", "PATH=" + os.Getenv("PATH")}
	cmd.Stdout = stdout
	cmd.Stderr = &limitedBuffer{limit: maxStderrOutput}
	return cmd.Run()
}

func parseVideoDataURL(dataURL string) ([]byte, error) {
	raw := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(raw, "data:") {
		return nil, fmt.Errorf("%w: not a data URL", ErrInvalidVideoAttachment)
	}
	raw = strings.TrimPrefix(raw, "data:")
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return nil, fmt.Errorf("%w: malformed data URL: missing comma", ErrInvalidVideoAttachment)
	}
	meta := strings.Split(raw[:comma], ";")
	mediaType := strings.ToLower(strings.TrimSpace(meta[0]))
	if !supportedVideoMediaTypes[mediaType] {
		return nil, fmt.Errorf("%w: unsupported media type %q", ErrInvalidVideoAttachment, mediaType)
	}
	base64Encoded := false
	for _, part := range meta[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			base64Encoded = true
			break
		}
	}
	if !base64Encoded {
		return nil, fmt.Errorf("%w: only base64-encoded data URLs are supported", ErrInvalidVideoAttachment)
	}
	payload := strings.TrimSpace(raw[comma+1:])
	if payload == "" {
		return nil, fmt.Errorf("%w: empty video data", ErrInvalidVideoAttachment)
	}
	if len(payload) > base64.StdEncoding.EncodedLen(maxVideoBytes) {
		return nil, fmt.Errorf("%w: video exceeds %d byte limit", ErrInvalidVideoAttachment, maxVideoBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid base64 video data: %v", ErrInvalidVideoAttachment, err)
	}
	if len(decoded) > maxVideoBytes {
		return nil, fmt.Errorf("%w: video exceeds %d byte limit", ErrInvalidVideoAttachment, maxVideoBytes)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("%w: empty video data", ErrInvalidVideoAttachment)
	}
	if !validVideoMagic(mediaType, decoded) {
		return nil, fmt.Errorf("%w: invalid %s video signature", ErrInvalidVideoAttachment, mediaType)
	}
	return decoded, nil
}

func validVideoMagic(mediaType string, video []byte) bool {
	switch mediaType {
	case "video/mp4", "video/quicktime":
		return len(video) >= 12 && string(video[4:8]) == "ftyp"
	case "video/webm":
		return len(video) >= 4 && bytes.Equal(video[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	default:
		return false
	}
}

func videoDemuxer(video []byte) string {
	if len(video) >= 4 && bytes.Equal(video[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return "matroska"
	}
	return "mov"
}
