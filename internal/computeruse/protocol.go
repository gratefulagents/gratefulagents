package computeruse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	MaxWire        = 12 << 20
	MaxScreenshot  = 8 << 20
	Lease          = 10 * time.Second
	RequestTimeout = 120 * time.Second
	ClaimTimeout   = 5 * time.Second
)

var ErrRejected = errors.New("computer use request rejected")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Action struct {
	Kind     string   `json:"kind"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
	DeltaX   *float64 `json:"deltaX,omitempty"`
	DeltaY   *float64 `json:"deltaY,omitempty"`
	Key      string   `json:"key,omitempty"`
	Text     string   `json:"text,omitempty"`
	Question string   `json:"question,omitempty"`
}

type Request struct {
	RequestID string `json:"requestId"`
	FrameID   string `json:"frameId,omitempty"`
	Action    Action `json:"action"`
}

type Geometry struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Capture struct {
	FrameID     string   `json:"frameId"`
	DataURL     string   `json:"dataUrl"`
	PixelWidth  int      `json:"pixelWidth"`
	PixelHeight int      `json:"pixelHeight"`
	Geometry    Geometry `json:"geometry"`
}

type Outcome struct {
	RequestID string   `json:"requestId"`
	Status    string   `json:"status"`
	Message   string   `json:"message"`
	Capture   *Capture `json:"capture,omitempty"`
}

// Exchange identity is supplied by the authenticated dashboard, never by the desktop.
type Exchange struct {
	Namespace string   `json:"namespace"`
	Run       string   `json:"run"`
	Owner     string   `json:"owner"`
	SessionID string   `json:"sessionId"`
	Operation string   `json:"operation"`
	RequestID string   `json:"requestId,omitempty"`
	Outcome   *Outcome `json:"outcome,omitempty"`
}

type Response struct {
	Active          bool     `json:"active"`
	Pending         *Request `json:"pending,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	VisionAvailable bool     `json:"visionAvailable"`
}

func Decode(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, MaxWire+1))
	if err != nil || len(b) > MaxWire {
		return ErrRejected
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrRejected
	}
	return nil
}

func (r Response) Validate() error {
	switch r.Reason {
	case "", "session inactive", "session stopped", "computer use request rejected":
	default:
		return ErrRejected
	}
	if r.Pending != nil {
		p := r.Pending
		if !r.Active || !identifier.MatchString(p.RequestID) || (p.FrameID != "" && !identifier.MatchString(p.FrameID)) || p.Action.Validate() != nil {
			return ErrRejected
		}
	}
	return nil
}

func (a Action) Validate() error {
	if len(a.Question) > 2048 || len(a.Key) > 40 || (a.Kind != "type" && a.Text != "") {
		return ErrRejected
	}
	for _, n := range []*float64{a.X, a.Y, a.DeltaX, a.DeltaY} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || math.Abs(*n) > 100000) {
			return ErrRejected
		}
	}
	switch a.Kind {
	case "observe":
		if a.X != nil || a.Y != nil || a.DeltaX != nil || a.DeltaY != nil || a.Key != "" {
			return ErrRejected
		}
	case "click":
		if a.X == nil || a.Y == nil || a.DeltaX != nil || a.DeltaY != nil || a.Key != "" || a.Question != "" {
			return ErrRejected
		}
	case "scroll":
		if a.DeltaX == nil || a.DeltaY == nil || a.X != nil || a.Y != nil || a.Key != "" || a.Question != "" {
			return ErrRejected
		}
		if math.Abs(*a.DeltaX) > 1000 || math.Abs(*a.DeltaY) > 1000 || math.Trunc(*a.DeltaX) != *a.DeltaX || math.Trunc(*a.DeltaY) != *a.DeltaY || (*a.DeltaX == 0 && *a.DeltaY == 0) {
			return ErrRejected
		}
	case "key":
		// Named keys only: this action cannot be used as a text input channel.
		switch a.Key {
		case "Enter", "Tab", "Shift+Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown":
		default:
			return ErrRejected
		}
		if a.X != nil || a.Y != nil || a.DeltaX != nil || a.DeltaY != nil || a.Question != "" {
			return ErrRejected
		}
	case "type":
		if a.Text == "" || len(a.Text) > 4000 || !utf8.ValidString(a.Text) || len(utf16.Encode([]rune(a.Text))) > 1000 || strings.ContainsFunc(a.Text, unicode.IsControl) {
			return ErrRejected
		}
		fallthrough
	case "activate":
		if a.X != nil || a.Y != nil || a.DeltaX != nil || a.DeltaY != nil || a.Key != "" || a.Question != "" {
			return ErrRejected
		}
	default:
		return ErrRejected
	}
	return nil
}

func (e Exchange) Validate() error {
	if !identifier.MatchString(e.Namespace) || !identifier.MatchString(e.Run) || !identifier.MatchString(e.SessionID) || len(e.Owner) == 0 || len(e.Owner) > 256 {
		return ErrRejected
	}
	switch e.Operation {
	case "attach", "poll", "stop":
		if e.RequestID != "" || e.Outcome != nil {
			return ErrRejected
		}
	case "claim":
		if !identifier.MatchString(e.RequestID) || e.Outcome != nil {
			return ErrRejected
		}
	case "resolve":
		if !identifier.MatchString(e.RequestID) || e.Outcome == nil || e.Outcome.RequestID != e.RequestID {
			return ErrRejected
		}
		if err := e.Outcome.Validate(); err != nil {
			return err
		}
	default:
		return ErrRejected
	}
	return nil
}

func (o Outcome) Validate() error {
	if len(o.Message) > 2048 {
		return ErrRejected
	}
	switch o.Status {
	case "completed", "failed", "denied":
	default:
		return ErrRejected
	}
	if o.Capture != nil {
		if o.Status != "completed" {
			return ErrRejected
		}
		_, err := o.Capture.PNG()
		return err
	}
	return nil
}

func (c Capture) PNG() ([]byte, error) {
	const prefix = "data:image/png;base64,"
	if !identifier.MatchString(c.FrameID) || !strings.HasPrefix(c.DataURL, prefix) || len(c.DataURL) > len(prefix)+base64.StdEncoding.EncodedLen(MaxScreenshot) {
		return nil, ErrRejected
	}
	g := c.Geometry
	for _, n := range []float64{g.X, g.Y, g.Width, g.Height} {
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 100000 {
			return nil, ErrRejected
		}
	}
	if g.Width <= 0 || g.Height <= 0 {
		return nil, ErrRejected
	}
	b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(c.DataURL, prefix))
	if err != nil || len(b) > MaxScreenshot {
		return nil, ErrRejected
	}
	config, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || config.Width != c.PixelWidth || config.Height != c.PixelHeight || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 64<<20 {
		return nil, ErrRejected
	}
	return b, nil
}
