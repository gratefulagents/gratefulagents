package computeruse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Protocol      = 2
	MaxWire       = 12 << 20
	MaxScreenshot = 8 << 20
	MaxErrorLen   = 1024
	MaxTypeChars  = 4000
	MaxURLLen     = 2048
	maxPixels     = 100000
)

// Reasons are the only strings a Response may carry; anything else is
// rejected so process output cannot smuggle data through the relay.
const (
	ReasonNoSession    = "no desktop session"
	ReasonForeignOwner = "another user is controlling this run's desktop"
	ReasonEnded        = "desktop session ended"
	ReasonDisconnected = "desktop disconnected"
	ReasonStaleResult  = "result does not match the delivered request"
	ReasonClosed       = "run is not accepting computer use"
	ReasonInvalid      = "invalid computer use request"
)

var reasons = map[string]bool{"": true, ReasonNoSession: true, ReasonForeignOwner: true, ReasonEnded: true, ReasonDisconnected: true, ReasonStaleResult: true, ReasonClosed: true, ReasonInvalid: true}

var (
	ErrRejected     = errors.New("invalid computer use request")
	ErrNoDesktop    = errors.New("no desktop connected")
	ErrBusy         = errors.New("another computer use action is in flight")
	ErrDisconnected = errors.New("desktop disconnected")
	ErrTimeout      = errors.New("desktop timed out")
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

// Action is the computer_use tool input and the action relayed to the desktop.
type Action struct {
	Action          string   `json:"action"`
	Coordinate      []int    `json:"coordinate,omitempty"`
	StartCoordinate []int    `json:"start_coordinate,omitempty"`
	Text            string   `json:"text,omitempty"`
	ScrollDirection string   `json:"scroll_direction,omitempty"`
	ScrollAmount    *int     `json:"scroll_amount,omitempty"`
	Repeat          *int     `json:"repeat,omitempty"`
	Duration        *float64 `json:"duration,omitempty"`
	Region          []int    `json:"region,omitempty"`
	URL             string   `json:"url,omitempty"`
}

const (
	fCoordinate      = "coordinate"
	fStartCoordinate = "start_coordinate"
	fText            = "text"
	fScrollDirection = "scroll_direction"
	fScrollAmount    = "scroll_amount"
	fRepeat          = "repeat"
	fDuration        = "duration"
	fRegion          = "region"
	fURL             = "url"
)

type actionSpec struct{ required, optional []string }

var clickSpec = actionSpec{required: []string{fCoordinate}, optional: []string{fText}}

var actionSpecs = map[string]actionSpec{
	"screenshot":      {},
	"left_click":      clickSpec,
	"right_click":     clickSpec,
	"middle_click":    clickSpec,
	"double_click":    clickSpec,
	"triple_click":    clickSpec,
	"mouse_move":      {required: []string{fCoordinate}},
	"left_click_drag": {required: []string{fStartCoordinate, fCoordinate}, optional: []string{fText}},
	"left_mouse_down": {optional: []string{fCoordinate}},
	"left_mouse_up":   {optional: []string{fCoordinate}},
	"scroll":          {required: []string{fScrollDirection}, optional: []string{fScrollAmount, fCoordinate, fText}},
	"type":            {required: []string{fText}},
	"key":             {required: []string{fText}, optional: []string{fRepeat}},
	"wait":            {optional: []string{fDuration}},
	"cursor_position": {},
	"zoom":            {required: []string{fRegion}},
	"open_url":        {required: []string{fURL}},
}

// ActionNames lists every valid action in schema order.
var ActionNames = []string{"screenshot", "left_click", "right_click", "middle_click", "double_click", "triple_click", "mouse_move", "left_click_drag", "left_mouse_down", "left_mouse_up", "scroll", "type", "key", "wait", "cursor_position", "zoom", "open_url"}

func (a Action) present() map[string]bool {
	return map[string]bool{
		fCoordinate:      a.Coordinate != nil,
		fStartCoordinate: a.StartCoordinate != nil,
		fText:            a.Text != "",
		fScrollDirection: a.ScrollDirection != "",
		fScrollAmount:    a.ScrollAmount != nil,
		fRepeat:          a.Repeat != nil,
		fDuration:        a.Duration != nil,
		fRegion:          a.Region != nil,
		fURL:             a.URL != "",
	}
}

// Validate enforces the per-action required and forbidden fields and ranges.
// Coordinates are only checked for shape here; CheckBounds compares them with
// the latest screenshot.
func (a Action) Validate() error {
	spec, ok := actionSpecs[a.Action]
	if !ok {
		return rejected("unknown action %q", a.Action)
	}
	present := a.present()
	allowed := map[string]bool{}
	for _, f := range spec.required {
		if !present[f] {
			return rejected("%s requires %s", a.Action, f)
		}
		allowed[f] = true
	}
	for _, f := range spec.optional {
		allowed[f] = true
	}
	for _, f := range []string{fCoordinate, fStartCoordinate, fText, fScrollDirection, fScrollAmount, fRepeat, fDuration, fRegion, fURL} {
		if present[f] && !allowed[f] {
			return rejected("%s does not accept %s", a.Action, f)
		}
	}
	if present[fCoordinate] {
		if err := checkPoint(fCoordinate, a.Coordinate); err != nil {
			return err
		}
	}
	if present[fStartCoordinate] {
		if err := checkPoint(fStartCoordinate, a.StartCoordinate); err != nil {
			return err
		}
	}
	switch a.Action {
	case "type":
		if !utf8.ValidString(a.Text) || utf8.RuneCountInString(a.Text) > MaxTypeChars {
			return rejected("type text must be valid UTF-8 with 1-%d characters", MaxTypeChars)
		}
		if strings.ContainsFunc(a.Text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' }) {
			return rejected("type text must not contain control characters other than newline and tab")
		}
	case "key":
		if _, err := ParseKeyChord(a.Text); err != nil {
			return err
		}
		if a.Repeat != nil && (*a.Repeat < 1 || *a.Repeat > 50) {
			return rejected("repeat must be 1-50")
		}
	case "scroll":
		switch a.ScrollDirection {
		case "up", "down", "left", "right":
		default:
			return rejected("scroll_direction must be up, down, left or right")
		}
		if a.ScrollAmount != nil && (*a.ScrollAmount < 1 || *a.ScrollAmount > 30) {
			return rejected("scroll_amount must be 1-30")
		}
	case "wait":
		if a.Duration != nil && !(*a.Duration >= 0.1 && *a.Duration <= 30) {
			return rejected("duration must be 0.1-30 seconds")
		}
	case "zoom":
		r := a.Region
		if len(r) != 4 || r[0] < 0 || r[1] < 0 || r[2] > maxPixels || r[3] > maxPixels || r[2] <= r[0] || r[3] <= r[1] {
			return rejected("region must be [x0, y0, x1, y1] with x1 > x0 >= 0 and y1 > y0 >= 0")
		}
	case "open_url":
		if _, err := WebURL(a.URL); err != nil {
			return err
		}
	}
	if present[fText] && a.Action != "type" && a.Action != "key" {
		if _, err := ParseModifiers(a.Text); err != nil {
			return err
		}
	}
	return nil
}

func checkPoint(name string, p []int) error {
	if len(p) != 2 || p[0] < 0 || p[1] < 0 || p[0] >= maxPixels || p[1] >= maxPixels {
		return rejected("%s must be [x, y] with non-negative integer pixels", name)
	}
	return nil
}

// CheckBounds verifies that every point lies on a screenshot of width×height.
func (a Action) CheckBounds(width, height int) error {
	for _, p := range []struct {
		name string
		v    []int
	}{{fCoordinate, a.Coordinate}, {fStartCoordinate, a.StartCoordinate}} {
		if len(p.v) == 2 && (p.v[0] >= width || p.v[1] >= height) {
			return rejected("%s (%d, %d) is outside the latest screenshot (%dx%d); use coordinates from the latest screenshot", p.name, p.v[0], p.v[1], width, height)
		}
	}
	if len(a.Region) == 4 && (a.Region[2] > width || a.Region[3] > height) {
		return rejected("region exceeds the latest screenshot (%dx%d)", width, height)
	}
	return nil
}

// WebURL accepts absolute http(s) URLs with a host and without credentials.
func WebURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLLen || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return nil, rejected("url must be an absolute http(s) URL of at most %d characters without spaces", MaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, rejected("url must be an absolute http(s) URL with a host and no credentials")
	}
	return u, nil
}

// KeyChord is a parsed key combination with canonical names.
type KeyChord struct {
	Cmd, Ctrl, Alt, Shift, Fn bool
	Key                       string
}

var modifierNames = map[string]string{
	"cmd": "cmd", "command": "cmd", "super": "cmd", "meta": "cmd", "win": "cmd",
	"ctrl": "ctrl", "control": "ctrl",
	"alt": "alt", "option": "alt", "opt": "alt",
	"shift": "shift",
	"fn":    "fn",
}

var keyNames = func() map[string]string {
	m := map[string]string{
		"return": "return", "enter": "return", "tab": "tab", "space": "space", "backspace": "backspace",
		"delete": "delete", "escape": "escape", "esc": "escape",
		"up": "up", "down": "down", "left": "left", "right": "right",
		"arrowup": "up", "arrowdown": "down", "arrowleft": "left", "arrowright": "right",
		"home": "home", "end": "end",
		"page_up": "pageup", "pageup": "pageup", "prior": "pageup",
		"page_down": "pagedown", "pagedown": "pagedown", "next": "pagedown",
		"capslock": "capslock",
		"minus":    "-", "equal": "=", "bracketleft": "[", "bracketright": "]", "semicolon": ";",
		"apostrophe": "'", "comma": ",", "period": ".", "slash": "/", "backslash": `\`, "grave": "`",
	}
	for i := 1; i <= 20; i++ {
		m[fmt.Sprintf("f%d", i)] = fmt.Sprintf("f%d", i)
	}
	// Shifted punctuation implies Shift on the desktop.
	for _, c := range "abcdefghijklmnopqrstuvwxyz0123456789-=[];',./\\`!@#$%^&*()_+{}|:\"<>?~" {
		m[string(c)] = string(c)
	}
	return m
}()

func (c *KeyChord) setModifier(token string) error {
	name, ok := modifierNames[strings.ToLower(token)]
	if !ok {
		return rejected("unknown modifier %q", token)
	}
	flag := map[string]*bool{"cmd": &c.Cmd, "ctrl": &c.Ctrl, "alt": &c.Alt, "shift": &c.Shift, "fn": &c.Fn}[name]
	if *flag {
		return rejected("duplicate modifier %q", token)
	}
	*flag = true
	return nil
}

// ParseKeyChord parses one chord such as "cmd+c", "Return" or
// "ctrl+shift+Tab". Matching is case-insensitive. The emergency-stop and
// force-quit chords are reserved.
func ParseKeyChord(s string) (KeyChord, error) {
	var c KeyChord
	if s == "" || len(s) > 64 {
		return c, rejected("key must be one chord such as cmd+c or Return")
	}
	prefix, last := "", s
	switch {
	case s == "+":
	case strings.HasSuffix(s, "++"):
		prefix, last = strings.TrimSuffix(s, "++"), "+"
	default:
		if i := strings.LastIndex(s, "+"); i >= 0 {
			prefix, last = s[:i], s[i+1:]
		}
	}
	if prefix != "" {
		for _, mod := range strings.Split(prefix, "+") {
			if err := c.setModifier(mod); err != nil {
				return c, err
			}
		}
	}
	if name, ok := modifierNames[strings.ToLower(last)]; ok {
		// A lone modifier press, e.g. "shift" or "cmd+shift".
		c.Key = name
		return c, nil
	}
	key, ok := keyNames[strings.ToLower(last)]
	if !ok {
		return c, rejected("unknown key %q", last)
	}
	c.Key = key
	if c.Cmd && c.Alt && c.Key == "escape" {
		return c, rejected("%s is reserved (emergency stop / force quit)", s)
	}
	return c, nil
}

// ParseModifiers parses held modifiers such as "shift" or "cmd+shift".
func ParseModifiers(s string) (KeyChord, error) {
	var c KeyChord
	if len(s) > 64 {
		return c, rejected("modifiers too long")
	}
	for _, mod := range strings.Split(s, "+") {
		if err := c.setModifier(mod); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Request is an action awaiting execution on the desktop.
type Request struct {
	ID     string `json:"id"`
	Action Action `json:"action"`
}

type Screenshot struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Result is the desktop's answer to a Request.
type Result struct {
	RequestID  string      `json:"requestId"`
	OK         bool        `json:"ok"`
	Error      string      `json:"error"`
	Denied     bool        `json:"denied"`
	Screenshot *Screenshot `json:"screenshot,omitempty"`
	Cursor     *Point      `json:"cursor,omitempty"`
}

// Exchange identity is supplied by the authenticated dashboard, never by the desktop.
type Exchange struct {
	Namespace string  `json:"namespace"`
	Run       string  `json:"run"`
	Owner     string  `json:"owner"`
	SessionID string  `json:"sessionId"`
	Operation string  `json:"operation"`
	RequestID string  `json:"requestId,omitempty"`
	Result    *Result `json:"result,omitempty"`
}

type Response struct {
	Protocol  int      `json:"protocol"`
	Active    bool     `json:"active"`
	Available bool     `json:"available"`
	Reason    string   `json:"reason"`
	Request   *Request `json:"request,omitempty"`
}

// Decode strictly decodes exactly one bounded JSON value.
func Decode(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, MaxWire+1))
	if err != nil || len(b) > MaxWire {
		return ErrRejected
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if d.Decode(new(any)) != io.EOF {
		return rejected("trailing data")
	}
	return nil
}

func (r Response) Validate() error {
	if r.Protocol != Protocol {
		return rejected("unsupported protocol %d", r.Protocol)
	}
	if !reasons[r.Reason] {
		return rejected("unknown reason")
	}
	if p := r.Request; p != nil {
		if !r.Active || !identifier.MatchString(p.ID) || p.Action.Action == "wait" {
			return rejected("invalid request")
		}
		if err := p.Action.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (e Exchange) Validate() error {
	if !identifier.MatchString(e.Namespace) || !identifier.MatchString(e.Run) || !identifier.MatchString(e.SessionID) || len(e.Owner) == 0 || len(e.Owner) > 256 {
		return rejected("invalid identity")
	}
	switch e.Operation {
	case "connect", "next", "disconnect":
		if e.RequestID != "" || e.Result != nil {
			return rejected("%s takes no request or result", e.Operation)
		}
	case "result":
		if !identifier.MatchString(e.RequestID) || e.Result == nil || e.Result.RequestID != e.RequestID {
			return rejected("result requires a matching requestId")
		}
		return e.Result.Validate()
	default:
		return rejected("unknown operation %q", e.Operation)
	}
	return nil
}

func (r Result) Validate() error {
	if !identifier.MatchString(r.RequestID) {
		return rejected("invalid requestId")
	}
	if r.OK {
		if r.Error != "" || r.Denied || r.Screenshot == nil {
			return rejected("successful result needs a screenshot and no error")
		}
	} else if r.Error == "" || len(r.Error) > MaxErrorLen || !utf8.ValidString(r.Error) {
		return rejected("failed result needs an error of 1-%d bytes", MaxErrorLen)
	}
	if r.Cursor != nil && (r.Cursor.X < -maxPixels || r.Cursor.X > maxPixels || r.Cursor.Y < -maxPixels || r.Cursor.Y > maxPixels) {
		return rejected("cursor out of range")
	}
	if r.Screenshot != nil {
		return r.Screenshot.Validate()
	}
	return nil
}

// Validate strictly decodes the image and checks its header against the
// declared media type and dimensions.
func (s Screenshot) Validate() error {
	var format string
	switch s.MediaType {
	case "image/jpeg":
		format = "jpeg"
	case "image/png":
		format = "png"
	default:
		return rejected("screenshot must be image/jpeg or image/png")
	}
	// The decoder skips CR/LF even in strict mode; the wire form has none.
	if len(s.Data) > base64.StdEncoding.EncodedLen(MaxScreenshot) || strings.ContainsAny(s.Data, "\r\n") {
		return rejected("screenshot too large")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s.Data)
	if err != nil || len(b) == 0 || len(b) > MaxScreenshot {
		return rejected("screenshot data must be standard base64 of at most %d bytes", MaxScreenshot)
	}
	cfg, got, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || got != format || cfg.Width != s.Width || cfg.Height != s.Height || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 16384 || cfg.Height > 16384 {
		return rejected("screenshot header does not match its media type and dimensions")
	}
	return nil
}
