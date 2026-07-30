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
	maxPacketLineBytes = 1024
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
	Streams []videoStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type videoStream struct {
	Index       int    `json:"index"`
	CodecType   string `json:"codec_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Duration    string `json:"duration"`
	Disposition struct {
		AttachedPicture int `json:"attached_pic"`
	} `json:"disposition"`
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

type packetDurationWriter struct {
	line    []byte
	minimum float64
	maximum float64
	found   bool
}

func (w *packetDurationWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			w.recordLine()
			w.line = w.line[:0]
			continue
		}
		if len(w.line) == maxPacketLineBytes {
			return 0, fmt.Errorf("ffprobe packet line exceeds %d byte limit", maxPacketLineBytes)
		}
		w.line = append(w.line, b)
	}
	return len(p), nil
}

func (w *packetDurationWriter) finish() {
	if len(w.line) > 0 {
		w.recordLine()
		w.line = w.line[:0]
	}
}

func (w *packetDurationWriter) recordLine() {
	pts, duration, ok := strings.Cut(strings.TrimSpace(string(w.line)), ",")
	if !ok {
		return
	}
	packetPTS, err := strconv.ParseFloat(strings.TrimSpace(pts), 64)
	if err != nil || math.IsNaN(packetPTS) || math.IsInf(packetPTS, 0) {
		return
	}
	packetDuration, err := strconv.ParseFloat(strings.TrimSpace(duration), 64)
	if err != nil || math.IsNaN(packetDuration) || math.IsInf(packetDuration, 0) || packetDuration < 0 {
		packetDuration = 0
	}
	end := packetPTS + packetDuration
	if !w.found {
		w.minimum = packetPTS
		w.maximum = end
		w.found = true
		return
	}
	if packetPTS < w.minimum {
		w.minimum = packetPTS
	}
	if end > w.maximum {
		w.maximum = end
	}
}

// ParseVideoDataURLs extracts JPEG frames from one video data URL.
func ParseVideoDataURLs(ctx context.Context, dataURLs []string, maxFrames int) ([]MessageImage, error) {
	dataURL, err := singleVideoDataURL(dataURLs)
	if err != nil {
		return nil, err
	}
	if dataURL == "" {
		return nil, nil
	}
	if err := validateVideoFrameCount(maxFrames); err != nil {
		return nil, err
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
	return extractVideoFrames(ctx, video, maxFrames)
}

func singleVideoDataURL(dataURLs []string) (string, error) {
	var dataURL string
	for _, value := range dataURLs {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if dataURL != "" {
			return "", fmt.Errorf("%w: too many videos: maximum 1 per message", ErrInvalidVideoAttachment)
		}
		dataURL = value
	}
	return dataURL, nil
}

func validateVideoFrameCount(maxFrames int) error {
	if maxFrames < 1 || maxFrames > maxVideoFrames {
		return fmt.Errorf("%w: max frames must be between 1 and %d", ErrInvalidVideoAttachment, maxVideoFrames)
	}
	return nil
}

func extractVideoFrames(ctx context.Context, video []byte, maxFrames int) ([]MessageImage, error) {
	tempParent := os.Getenv("VIDEO_TMP_DIR")
	if tempParent == "" {
		tempParent = os.TempDir()
	}
	dir, err := os.MkdirTemp(tempParent, "sessionclient-video-*")
	if err != nil {
		return nil, fmt.Errorf("create video temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	inputPath := filepath.Join(dir, "input")
	if err := os.WriteFile(inputPath, video, 0o600); err != nil {
		return nil, fmt.Errorf("write video input: %w", err)
	}

	demuxer := videoDemuxer(video)
	stream, duration, err := probeVideo(ctx, demuxer, inputPath)
	if err != nil {
		return nil, err
	}

	outputPattern := filepath.Join(dir, "frame-%06d.jpg")
	filter := fmt.Sprintf("fps=%g,scale=1600:1600:force_original_aspect_ratio=decrease", float64(maxFrames)/duration)
	ffmpegCtx, cancelFFmpeg := context.WithTimeout(ctx, ffmpegTimeout)
	ffmpegOutput := &limitedBuffer{limit: maxProbeOutput}
	ffmpegErr := runVideoCommand(ffmpegCtx, "ffmpeg", ffmpegOutput, "-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe", "-threads", "1", "-f", demuxer, "-i", inputPath, "-map", fmt.Sprintf("0:%d", stream.Index), "-filter_threads", "1", "-vf", filter, "-q:v", "4", "-frames:v", strconv.Itoa(maxFrames), outputPattern)
	ffmpegContextErr := ffmpegCtx.Err()
	cancelFFmpeg()
	if ffmpegErr != nil {
		return nil, videoCommandError(ffmpegContextErr, ffmpegErr, "ffmpeg", "ffmpeg frame extraction failed")
	}
	return readVideoFrames(dir, maxFrames)
}

func readVideoFrames(dir string, maxFrames int) ([]MessageImage, error) {
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

func probeVideo(ctx context.Context, demuxer, inputPath string) (videoStream, float64, error) {
	probeCtx, cancelProbe := context.WithTimeout(ctx, ffprobeTimeout)
	probeOutput := &limitedBuffer{limit: maxProbeOutput}
	probeErr := runVideoCommand(probeCtx, "ffprobe", probeOutput, "-v", "error", "-protocol_whitelist", "file,pipe", "-f", demuxer, "-i", inputPath, "-show_entries", "stream=index,codec_type,width,height,duration:stream_disposition=attached_pic:format=duration", "-of", "json")
	probeContextErr := probeCtx.Err()
	cancelProbe()
	if probeErr != nil {
		return videoStream{}, 0, videoCommandError(probeContextErr, probeErr, "ffprobe", "ffprobe failed")
	}
	if probeOutput.truncated {
		return videoStream{}, 0, fmt.Errorf("%w: ffprobe output exceeds %d byte limit", ErrInvalidVideoAttachment, maxProbeOutput)
	}

	var probe videoProbe
	if err := json.Unmarshal(probeOutput.Bytes(), &probe); err != nil {
		return videoStream{}, 0, fmt.Errorf("%w: decode ffprobe output: %v", ErrInvalidVideoAttachment, err)
	}
	stream, err := selectedVideoStream(probe.Streams)
	if err != nil {
		return videoStream{}, 0, err
	}
	duration, err := probeDuration(ctx, demuxer, inputPath, probe.Format.Duration, stream)
	if err != nil {
		return videoStream{}, 0, err
	}
	return stream, duration, nil
}

func selectedVideoStream(streams []videoStream) (videoStream, error) {
	for _, stream := range streams {
		if stream.CodecType == "video" && stream.Disposition.AttachedPicture == 0 {
			if stream.Width <= 0 || stream.Height <= 0 || stream.Width > maxVideoDimension || stream.Height > maxVideoDimension || int64(stream.Width)*int64(stream.Height) > maxVideoPixels {
				return videoStream{}, fmt.Errorf("%w: invalid video dimensions %dx%d", ErrInvalidVideoAttachment, stream.Width, stream.Height)
			}
			return stream, nil
		}
	}
	return videoStream{}, fmt.Errorf("%w: video has no video stream", ErrInvalidVideoAttachment)
}

func probeDuration(ctx context.Context, demuxer, inputPath, formatDuration string, stream videoStream) (float64, error) {
	duration, available, err := parseProbeDuration(formatDuration)
	if err != nil {
		return 0, invalidVideoDuration(formatDuration)
	}
	if !available {
		duration, available, err = parseProbeDuration(stream.Duration)
		if err != nil {
			return 0, invalidVideoDuration(stream.Duration)
		}
	}
	if !available {
		duration, err = probePacketDuration(ctx, demuxer, inputPath, stream.Index)
		if err != nil {
			return 0, err
		}
	}
	if !validVideoDuration(duration) {
		return 0, invalidVideoDuration(formatDuration)
	}
	return duration, nil
}

func parseProbeDuration(value string) (float64, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "N/A") {
		return 0, false, nil
	}
	duration, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, true, err
	}
	return duration, true, nil
}

func probePacketDuration(ctx context.Context, demuxer, inputPath string, streamIndex int) (float64, error) {
	probeCtx, cancelProbe := context.WithTimeout(ctx, ffprobeTimeout)
	packets := &packetDurationWriter{}
	probeErr := runVideoCommand(probeCtx, "ffprobe", packets, "-v", "error", "-protocol_whitelist", "file,pipe", "-f", demuxer, "-i", inputPath, "-select_streams", strconv.Itoa(streamIndex), "-show_entries", "packet=pts_time,duration_time", "-of", "csv=p=0")
	probeContextErr := probeCtx.Err()
	cancelProbe()
	if probeErr != nil {
		return 0, videoCommandError(probeContextErr, probeErr, "ffprobe", "ffprobe failed")
	}
	packets.finish()
	if !packets.found {
		return 0, invalidVideoDuration("")
	}
	return packets.maximum - packets.minimum, nil
}

func validVideoDuration(duration float64) bool {
	return !math.IsNaN(duration) && !math.IsInf(duration, 0) && duration > 0 && duration <= maxVideoDuration.Seconds()
}

func invalidVideoDuration(value string) error {
	return fmt.Errorf("%w: invalid video duration %q", ErrInvalidVideoAttachment, value)
}

func videoCommandError(contextErr, commandErr error, name, failure string) error {
	if contextErr != nil {
		return contextErr
	}
	if errors.Is(commandErr, exec.ErrNotFound) {
		return fmt.Errorf("run %s: %w", name, commandErr)
	}
	return fmt.Errorf("%w: %s", ErrInvalidVideoAttachment, failure)
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
	metaValue, payload, ok := strings.Cut(raw, ",")
	if !ok {
		return nil, fmt.Errorf("%w: malformed data URL: missing comma", ErrInvalidVideoAttachment)
	}
	meta := strings.Split(metaValue, ";")
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
	payload = strings.TrimSpace(payload)
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
