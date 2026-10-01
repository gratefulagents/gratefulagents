package computeruse

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func encodeImage(t testing.TB, format string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func ip(n int) *int         { return &n }
func fp(f float64) *float64 { return &f }
func pt(x, y int) []int     { return []int{x, y} }
func region(r ...int) []int { return r }
func shot(t testing.TB, w, h int) *Screenshot {
	return &Screenshot{MediaType: "image/jpeg", Data: encodeImage(t, "jpeg", w, h), Width: w, Height: h}
}

func TestProtocolActionValidate(t *testing.T) {
	valid := []Action{
		{Action: "screenshot"},
		{Action: "left_click", Coordinate: pt(0, 0)},
		{Action: "right_click", Coordinate: pt(10, 20), Text: "cmd+shift"},
		{Action: "middle_click", Coordinate: pt(1, 1)},
		{Action: "double_click", Coordinate: pt(1, 1), Text: "Option"},
		{Action: "triple_click", Coordinate: pt(1, 1)},
		{Action: "mouse_move", Coordinate: pt(5, 5)},
		{Action: "left_click_drag", StartCoordinate: pt(1, 1), Coordinate: pt(9, 9), Text: "shift"},
		{Action: "left_mouse_down"},
		{Action: "left_mouse_up", Coordinate: pt(3, 4)},
		{Action: "scroll", ScrollDirection: "down"},
		{Action: "scroll", ScrollDirection: "left", ScrollAmount: ip(30), Coordinate: pt(3, 3), Text: "ctrl"},
		{Action: "type", Text: "hello\nworld\t✓"},
		{Action: "type", Text: strings.Repeat("é", MaxTypeChars)},
		{Action: "key", Text: "cmd+c"},
		{Action: "key", Text: "Return", Repeat: ip(50)},
		{Action: "wait"},
		{Action: "wait", Duration: fp(0.1)},
		{Action: "wait", Duration: fp(30)},
		{Action: "cursor_position"},
		{Action: "zoom", Region: region(0, 0, 10, 10)},
		{Action: "open_url", URL: "https://example.com/path?q=1"},
	}
	for _, a := range valid {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	invalid := map[string]Action{
		"unknown":               {Action: "observe"},
		"empty":                 {},
		"click without coord":   {Action: "left_click"},
		"click bad coord len":   {Action: "left_click", Coordinate: []int{1}},
		"click negative":        {Action: "left_click", Coordinate: pt(-1, 0)},
		"click bad modifier":    {Action: "left_click", Coordinate: pt(1, 1), Text: "hello"},
		"click dup modifier":    {Action: "left_click", Coordinate: pt(1, 1), Text: "cmd+command"},
		"click with url":        {Action: "left_click", Coordinate: pt(1, 1), URL: "https://x.y"},
		"screenshot with coord": {Action: "screenshot", Coordinate: pt(1, 1)},
		"move with text":        {Action: "mouse_move", Coordinate: pt(1, 1), Text: "shift"},
		"drag without start":    {Action: "left_click_drag", Coordinate: pt(1, 1)},
		"scroll no direction":   {Action: "scroll"},
		"scroll bad direction":  {Action: "scroll", ScrollDirection: "sideways"},
		"scroll amount 0":       {Action: "scroll", ScrollDirection: "up", ScrollAmount: ip(0)},
		"scroll amount 31":      {Action: "scroll", ScrollDirection: "up", ScrollAmount: ip(31)},
		"type empty":            {Action: "type"},
		"type too long":         {Action: "type", Text: strings.Repeat("a", MaxTypeChars+1)},
		"type control":          {Action: "type", Text: "a\x00b"},
		"type invalid utf8":     {Action: "type", Text: "\xff"},
		"type with repeat":      {Action: "type", Text: "a", Repeat: ip(2)},
		"key bad":               {Action: "key", Text: "cmd+nope"},
		"key reserved":          {Action: "key", Text: "ctrl+alt+cmd+Escape"},
		"key repeat 0":          {Action: "key", Text: "a", Repeat: ip(0)},
		"key repeat 51":         {Action: "key", Text: "a", Repeat: ip(51)},
		"wait short":            {Action: "wait", Duration: fp(0.05)},
		"wait long":             {Action: "wait", Duration: fp(30.5)},
		"wait coord":            {Action: "wait", Coordinate: pt(1, 1)},
		"cursor with text":      {Action: "cursor_position", Text: "x"},
		"zoom missing":          {Action: "zoom"},
		"zoom inverted":         {Action: "zoom", Region: region(10, 0, 5, 10)},
		"zoom flat":             {Action: "zoom", Region: region(0, 5, 10, 5)},
		"zoom short":            {Action: "zoom", Region: region(0, 0, 10)},
		"url ftp":               {Action: "open_url", URL: "ftp://example.com"},
		"url relative":          {Action: "open_url", URL: "/path"},
		"url credentials":       {Action: "open_url", URL: "https://user:pw@example.com"},
		"url space":             {Action: "open_url", URL: "https://example.com/a b"},
		"url too long":          {Action: "open_url", URL: "https://example.com/" + strings.Repeat("a", MaxURLLen)},
		"url with coord":        {Action: "open_url", URL: "https://example.com", Coordinate: pt(1, 1)},
	}
	for name, a := range invalid {
		if err := a.Validate(); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: accepted %+v (%v)", name, a, err)
		}
	}
}

func TestProtocolCheckBounds(t *testing.T) {
	if err := (Action{Action: "left_click", Coordinate: pt(99, 49)}).CheckBounds(100, 50); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Action{
		{Action: "left_click", Coordinate: pt(100, 0)},
		{Action: "left_click", Coordinate: pt(0, 50)},
		{Action: "left_click_drag", StartCoordinate: pt(200, 0), Coordinate: pt(1, 1)},
		{Action: "zoom", Region: region(0, 0, 101, 10)},
	} {
		if err := a.CheckBounds(100, 50); !errors.Is(err, ErrRejected) {
			t.Errorf("accepted out-of-bounds %+v", a)
		}
	}
}

func TestChordParseKeyChord(t *testing.T) {
	for in, want := range map[string]KeyChord{
		"cmd+c":                {Cmd: true, Key: "c"},
		"CMD+C":                {Cmd: true, Key: "c"},
		"Return":               {Key: "return"},
		"enter":                {Key: "return"},
		"ctrl+shift+Tab":       {Ctrl: true, Shift: true, Key: "tab"},
		"control+option+Esc":   {Ctrl: true, Alt: true, Key: "escape"},
		"super+space":          {Cmd: true, Key: "space"},
		"meta+ArrowUp":         {Cmd: true, Key: "up"},
		"win+Page_Down":        {Cmd: true, Key: "pagedown"},
		"opt+Prior":            {Alt: true, Key: "pageup"},
		"fn+F20":               {Fn: true, Key: "f20"},
		"BackSpace":            {Key: "backspace"},
		"Delete":               {Key: "delete"},
		"CapsLock":             {Key: "capslock"},
		"cmd+minus":            {Cmd: true, Key: "-"},
		"cmd+=":                {Cmd: true, Key: "="},
		"cmd+bracketleft":      {Cmd: true, Key: "["},
		"cmd+grave":            {Cmd: true, Key: "`"},
		"cmd+backslash":        {Cmd: true, Key: `\`},
		"shift+9":              {Shift: true, Key: "9"},
		"cmd+alt+Escape+":      {},
		"cmd+alt+Escape":       {},
		"ctrl+alt+cmd+esc":     {},
		"cmd+shift+alt+ESCAPE": {},
		"F21":                  {},
		"cmd+":                 {},
		"+":                    {Key: "+"},
		"cmd++":                {Cmd: true, Key: "+"},
		"cmd+?":                {Cmd: true, Key: "?"},
		"cmd+shift":            {Cmd: true, Key: "shift"},
		"cmd+cmd+c":            {},
		"hyper+c":              {},
		"cmd":                  {Key: "cmd"},
		"":                     {},
		"ab":                   {},
	} {
		got, err := ParseKeyChord(in)
		if want.Key == "" {
			if !errors.Is(err, ErrRejected) {
				t.Errorf("%q accepted: %+v", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q = %+v, %v; want %+v", in, got, err, want)
		}
	}
}

func TestProtocolResultValidate(t *testing.T) {
	jpegShot := shot(t, 40, 30)
	pngShot := &Screenshot{MediaType: "image/png", Data: encodeImage(t, "png", 8, 6), Width: 8, Height: 6}
	valid := []Result{
		{RequestID: "r1", OK: true, Screenshot: jpegShot},
		{RequestID: "r1", OK: true, Screenshot: pngShot, Cursor: &Point{X: -5, Y: 10}},
		{RequestID: "r1", Error: "boom"},
		{RequestID: "r1", Error: "denied by user", Denied: true, Screenshot: jpegShot},
	}
	for _, r := range valid {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	invalid := map[string]Result{
		"bad id":              {RequestID: "../x", OK: true, Screenshot: jpegShot},
		"ok no screenshot":    {RequestID: "r1", OK: true},
		"ok with error":       {RequestID: "r1", OK: true, Error: "x", Screenshot: jpegShot},
		"ok denied":           {RequestID: "r1", OK: true, Denied: true, Screenshot: jpegShot},
		"fail no error":       {RequestID: "r1"},
		"fail long error":     {RequestID: "r1", Error: strings.Repeat("e", MaxErrorLen+1)},
		"wrong dims":          {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/jpeg", Data: jpegShot.Data, Width: 41, Height: 30}},
		"wrong media type":    {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/png", Data: jpegShot.Data, Width: 40, Height: 30}},
		"gif":                 {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/gif", Data: jpegShot.Data, Width: 40, Height: 30}},
		"data url":            {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/jpeg", Data: "data:image/jpeg;base64," + jpegShot.Data, Width: 40, Height: 30}},
		"not base64":          {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/jpeg", Data: "!!!", Width: 40, Height: 30}},
		"base64 not strict":   {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/jpeg", Data: jpegShot.Data + "\n", Width: 40, Height: 30}},
		"garbage image":       {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString([]byte("hello")), Width: 40, Height: 30}},
		"oversized":           {RequestID: "r1", OK: true, Screenshot: &Screenshot{MediaType: "image/jpeg", Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxScreenshot)+4), Width: 40, Height: 30}},
		"cursor out of range": {RequestID: "r1", OK: true, Screenshot: jpegShot, Cursor: &Point{X: 1 << 30}},
	}
	for name, r := range invalid {
		if err := r.Validate(); !errors.Is(err, ErrRejected) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestProtocolExchangeAndResponseValidate(t *testing.T) {
	base := Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1"}
	for _, op := range []string{"connect", "next", "disconnect"} {
		e := base
		e.Operation = op
		if err := e.Validate(); err != nil {
			t.Errorf("%s: %v", op, err)
		}
		e.RequestID = "r1"
		if e.Validate() == nil {
			t.Errorf("%s accepted requestId", op)
		}
	}
	e := base
	e.Operation, e.RequestID, e.Result = "result", "r1", &Result{RequestID: "r1", Error: "x"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Result = &Result{RequestID: "r2", Error: "x"}
	if e.Validate() == nil {
		t.Fatal("accepted mismatched result id")
	}
	for _, bad := range []Exchange{
		{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1", Operation: "attach_desktop"},
		{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1", Operation: "poll"},
		{Namespace: "ns", Run: "run", Owner: "", SessionID: "s1", Operation: "connect"},
		{Namespace: "ns", Run: "../run", Owner: "alice", SessionID: "s1", Operation: "connect"},
		{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1", Operation: "result", RequestID: "r1"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	good := Response{Protocol: 2, Active: true, Available: true, Request: &Request{ID: "r1", Action: Action{Action: "screenshot"}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Response{
		{Protocol: 1, Active: true},
		{Protocol: 2, Reason: "PRIVATE"},
		{Protocol: 2, Request: &Request{ID: "r1", Action: Action{Action: "screenshot"}}},
		{Protocol: 2, Active: true, Request: &Request{ID: "r1", Action: Action{Action: "wait"}}},
		{Protocol: 2, Active: true, Request: &Request{ID: "r1", Action: Action{Action: "left_click"}}},
		{Protocol: 2, Active: true, Request: &Request{ID: "", Action: Action{Action: "screenshot"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestProtocolDecodeStrict(t *testing.T) {
	var a Action
	if err := Decode(strings.NewReader(`{"action":"left_click","coordinate":[1,2]}`), &a); err != nil || a.Coordinate[1] != 2 {
		t.Fatal(err)
	}
	for _, in := range []string{`{"action":"screenshot","frameId":"x"}`, `{"action":"screenshot"} {}`, `{"action":"left_click","coordinate":[1.5,2]}`} {
		if Decode(strings.NewReader(in), &a) == nil {
			t.Errorf("accepted %s", in)
		}
	}
}
