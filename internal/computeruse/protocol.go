package computeruse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"math"
	"net/url"
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
	// ClaimTimeout bounds the time between a desktop claiming a request and
	// resolving it. Observation must fit a native capture plus the PNG upload;
	// typing must fit per-character native delivery; other input is quick.
	ClaimTimeout        = 15 * time.Second
	ObserveClaimTimeout = 30 * time.Second
	TypeClaimTimeout    = 60 * time.Second
)

var ErrRejected = errors.New("computer use request rejected")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// ClaimTimeoutFor returns how long a claimed request of this kind may stay unresolved.
func ClaimTimeoutFor(a Action) time.Duration {
	switch a.Kind {
	case "observe":
		return ObserveClaimTimeout
	case "type":
		return TypeClaimTimeout
	}
	return ClaimTimeout
}

// visibleText reports whether every rune is printable so the supervisor
// approves exactly the text that will be typed: control, format (zero-width
// space, BOM, bidi overrides and isolates), line/paragraph separators,
// private-use, and unassigned code points are rejected. Zero-width joiner and
// non-joiner stay allowed because emoji sequences and several scripts need them.
func visibleText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
		return !unicode.IsGraphic(r) && r != '\u200c' && r != '\u200d'
	})
}

type Action struct {
	Kind string `json:"kind"`
	// X/Y are frame pixels for click, move, drag start, and optional scroll position.
	X *float64 `json:"x,omitempty"`
	Y *float64 `json:"y,omitempty"`
	// ToX/ToY are the drag destination in frame pixels.
	ToX    *float64 `json:"toX,omitempty"`
	ToY    *float64 `json:"toY,omitempty"`
	DeltaX *float64 `json:"deltaX,omitempty"`
	DeltaY *float64 `json:"deltaY,omitempty"`
	// Button is left (default), right, or middle; Count is 1 (default), 2, or 3.
	Button string `json:"button,omitempty"`
	Count  *int   `json:"count,omitempty"`
	// Seconds is the agent-side wait before a fresh observation; never sent to the desktop.
	Seconds *int   `json:"seconds,omitempty"`
	Key     string `json:"key,omitempty"`
	Text    string `json:"text,omitempty"`
	// URL is an absolute http(s) address for open_url, loaded by the approved
	// browser in the background.
	URL      string `json:"url,omitempty"`
	Question string `json:"question,omitempty"`
}

// NeedsFrame reports whether the action targets coordinates or focus from a
// prior observation and therefore requires its frameId.
func (a Action) NeedsFrame() bool {
	switch a.Kind {
	case "observe", "wait", "open_url":
		return false
	}
	return true
}

// WebURL accepts absolute http(s) URLs with a host and without credentials.
func WebURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 2048 || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return nil, ErrRejected
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, ErrRejected
	}
	return u, nil
}

// IsInput reports whether the action delivers OS events to the approved window.
func (a Action) IsInput() bool {
	switch a.Kind {
	case "observe", "wait":
		return false
	}
	return true
}

// Hotkey is a parsed key action: a base key plus modifier set.
type Hotkey struct {
	Control, Option, Shift, Cmd bool
	Key                         string
}

// Modifiers returns the canonical modifier prefix, e.g. "Control+Option+Shift+Cmd+".
func (h Hotkey) String() string {
	var b strings.Builder
	for _, m := range []struct {
		on   bool
		name string
	}{{h.Control, "Control"}, {h.Option, "Option"}, {h.Shift, "Shift"}, {h.Cmd, "Cmd"}} {
		if m.on {
			b.WriteString(m.name)
			b.WriteByte('+')
		}
	}
	b.WriteString(h.Key)
	return b.String()
}

var namedKeys = map[string]bool{"Enter": true, "Tab": true, "Escape": true, "Backspace": true, "Delete": true, "ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true, "Home": true, "End": true, "PageUp": true, "PageDown": true, "Space": true}

// ParseHotkey accepts "Mod+...+Key" in any modifier order. Base keys are the
// named navigation/editing keys, Space, A-Z, and 0-9. Letters and digits need
// Control, Option, or Cmd so this action cannot become a text input channel
// that bypasses proposed-text review. Combinations that leave the approved
// window or the session (quit, close, hide, minimize, app/space switching,
// Spotlight, screenshots, force quit, the emergency stop, fullscreen, Dock)
// are rejected here and again natively.
func ParseHotkey(key string) (Hotkey, error) {
	var h Hotkey
	if len(key) == 0 || len(key) > 40 {
		return h, ErrRejected
	}
	parts := strings.Split(key, "+")
	for i, part := range parts {
		last := i == len(parts)-1
		switch {
		case !last && part == "Control" || !last && part == "Ctrl":
			if h.Control {
				return h, ErrRejected
			}
			h.Control = true
		case !last && part == "Option" || !last && part == "Alt":
			if h.Option {
				return h, ErrRejected
			}
			h.Option = true
		case !last && part == "Shift":
			if h.Shift {
				return h, ErrRejected
			}
			h.Shift = true
		case !last && part == "Cmd" || !last && part == "Command" || !last && part == "Meta":
			if h.Cmd {
				return h, ErrRejected
			}
			h.Cmd = true
		case last && namedKeys[part]:
			h.Key = part
		case last && len(part) == 1 && (part[0] >= 'A' && part[0] <= 'Z' || part[0] >= 'a' && part[0] <= 'z' || part[0] >= '0' && part[0] <= '9'):
			h.Key = strings.ToUpper(part)
			if !h.Control && !h.Option && !h.Cmd {
				return h, ErrRejected
			}
		default:
			return h, ErrRejected
		}
	}
	if h.denied() {
		return h, ErrRejected
	}
	return h, nil
}

func (h Hotkey) denied() bool {
	arrow := strings.HasPrefix(h.Key, "Arrow")
	switch {
	case h.Cmd && (h.Key == "Q" || h.Key == "W" || h.Key == "H" || h.Key == "M" || h.Key == "Tab" || h.Key == "Space" || h.Key == "Escape"):
		return true
	case h.Cmd && h.Shift && (h.Key == "3" || h.Key == "4" || h.Key == "5" || h.Key == "6"):
		return true
	case h.Cmd && h.Option && h.Key == "D", h.Cmd && h.Control && h.Key == "F":
		return true
	case h.Control && arrow, h.Control && h.Key == "Space":
		return true
	}
	return false
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
		if !r.Active || p.Action.Kind == "wait" || !identifier.MatchString(p.RequestID) || (p.FrameID != "" && !identifier.MatchString(p.FrameID)) || p.Action.Validate() != nil {
			return ErrRejected
		}
	}
	return nil
}

func (a Action) Validate() error {
	// Question belongs to observation only; the tool keeps any verification
	// question for its follow-up observation and never sends it with input.
	if len(a.Question) > 2048 || len(a.Key) > 40 || (a.Kind != "type" && a.Text != "") || (a.Kind != "observe" && a.Question != "") {
		return ErrRejected
	}
	for _, n := range []*float64{a.X, a.Y, a.ToX, a.ToY, a.DeltaX, a.DeltaY} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || math.Abs(*n) > 100000) {
			return ErrRejected
		}
	}
	// Fields that belong to exactly one kind.
	if (a.Kind != "click" && (a.Button != "" || a.Count != nil)) || (a.Kind != "drag" && (a.ToX != nil || a.ToY != nil)) ||
		(a.Kind != "wait" && a.Seconds != nil) || (a.Kind != "key" && a.Key != "") || (a.Kind != "scroll" && (a.DeltaX != nil || a.DeltaY != nil)) ||
		(a.Kind != "open_url" && a.URL != "") {
		return ErrRejected
	}
	point := a.X != nil && a.Y != nil
	if a.X == nil != (a.Y == nil) || a.ToX == nil != (a.ToY == nil) {
		return ErrRejected
	}
	switch a.Kind {
	case "observe":
		if point {
			return ErrRejected
		}
	case "wait":
		if point || a.Seconds == nil || *a.Seconds < 1 || *a.Seconds > 10 {
			return ErrRejected
		}
	case "click":
		if !point {
			return ErrRejected
		}
		switch a.Button {
		case "", "left", "right", "middle":
		default:
			return ErrRejected
		}
		if a.Count != nil && (*a.Count < 1 || *a.Count > 3) {
			return ErrRejected
		}
	case "move":
		if !point {
			return ErrRejected
		}
	case "drag":
		if !point || a.ToX == nil || (*a.X == *a.ToX && *a.Y == *a.ToY) {
			return ErrRejected
		}
	case "scroll":
		if a.DeltaX == nil || a.DeltaY == nil {
			return ErrRejected
		}
		if math.Abs(*a.DeltaX) > 1000 || math.Abs(*a.DeltaY) > 1000 || math.Trunc(*a.DeltaX) != *a.DeltaX || math.Trunc(*a.DeltaY) != *a.DeltaY || (*a.DeltaX == 0 && *a.DeltaY == 0) {
			return ErrRejected
		}
	case "key":
		if point {
			return ErrRejected
		}
		if _, err := ParseHotkey(a.Key); err != nil {
			return err
		}
	case "type":
		if point || a.Text == "" || len(a.Text) > 4000 || !visibleText(a.Text) || len(utf16.Encode([]rune(a.Text))) > 1000 {
			return ErrRejected
		}
	case "activate":
		if point {
			return ErrRejected
		}
	case "open_url":
		if point {
			return ErrRejected
		}
		if _, err := WebURL(a.URL); err != nil {
			return err
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
