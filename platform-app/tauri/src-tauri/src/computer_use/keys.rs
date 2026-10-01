//! Key names → macOS virtual key codes (ANSI layout) and modifier flags.

pub const KEY_RETURN: u16 = 36;
pub const KEY_TAB: u16 = 48;
pub const KEY_ESCAPE: u16 = 53;

/// Text is typed in chunks of at most this many UTF-16 units.
pub const TYPE_CHUNK_UNITS: usize = 16;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Modifier {
    Cmd,
    Ctrl,
    Alt,
    Shift,
    Fn,
}

impl Modifier {
    /// Press order; release runs in reverse.
    pub const ALL: [Modifier; 5] = [
        Modifier::Cmd,
        Modifier::Ctrl,
        Modifier::Alt,
        Modifier::Shift,
        Modifier::Fn,
    ];

    pub fn keycode(self) -> u16 {
        match self {
            Modifier::Cmd => 55,
            Modifier::Shift => 56,
            Modifier::Alt => 58,
            Modifier::Ctrl => 59,
            Modifier::Fn => 63,
        }
    }

    /// The matching `kCGEventFlagMask*` bit.
    pub fn flag(self) -> u64 {
        match self {
            Modifier::Shift => 0x0002_0000,
            Modifier::Ctrl => 0x0004_0000,
            Modifier::Alt => 0x0008_0000,
            Modifier::Cmd => 0x0010_0000,
            Modifier::Fn => 0x0080_0000,
        }
    }

    fn bit(self) -> u8 {
        match self {
            Modifier::Cmd => 1,
            Modifier::Ctrl => 2,
            Modifier::Alt => 4,
            Modifier::Shift => 8,
            Modifier::Fn => 16,
        }
    }

    pub fn parse(name: &str) -> Option<Modifier> {
        Some(match name.trim().to_ascii_lowercase().as_str() {
            "cmd" | "command" | "super" | "meta" | "win" | "super_l" | "super_r" | "meta_l"
            | "meta_r" => Modifier::Cmd,
            "ctrl" | "control" | "control_l" | "control_r" => Modifier::Ctrl,
            "alt" | "option" | "opt" | "alt_l" | "alt_r" => Modifier::Alt,
            "shift" | "shift_l" | "shift_r" => Modifier::Shift,
            "fn" => Modifier::Fn,
            _ => return None,
        })
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Modifiers(u8);

impl Modifiers {
    pub fn insert(&mut self, modifier: Modifier) {
        self.0 |= modifier.bit();
    }

    pub fn contains(self, modifier: Modifier) -> bool {
        self.0 & modifier.bit() != 0
    }

    pub fn iter(self) -> impl DoubleEndedIterator<Item = Modifier> {
        Modifier::ALL.into_iter().filter(move |m| self.contains(*m))
    }

    /// Parses held modifiers such as `"shift"` or `"cmd+shift"`. Empty means none.
    pub fn parse(text: &str) -> Result<Modifiers, String> {
        let mut modifiers = Modifiers::default();
        if text.trim().is_empty() {
            return Ok(modifiers);
        }
        for token in text.split('+') {
            let modifier = Modifier::parse(token)
                .ok_or_else(|| format!("`{}` is not a modifier key", token.trim()))?;
            modifiers.insert(modifier);
        }
        Ok(modifiers)
    }
}

/// One key press with held modifiers.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Chord {
    pub modifiers: Modifiers,
    pub key: u16,
    /// Set when the pressed key is itself a modifier (e.g. `"shift"` alone).
    pub key_modifier: Option<Modifier>,
}

impl Chord {
    pub fn parse(text: &str) -> Result<Chord, String> {
        let text = text.trim();
        if text.is_empty() {
            return Err("key chord is empty".into());
        }
        let (prefix, key_name) = if text == "+" {
            ("", "+")
        } else if let Some(prefix) = text.strip_suffix("++") {
            (prefix, "+")
        } else {
            match text.rsplit_once('+') {
                Some((prefix, key)) => (prefix, key),
                None => ("", text),
            }
        };
        let key_name = key_name.trim();
        if key_name.is_empty() {
            return Err(format!("invalid key chord `{text}`"));
        }

        let mut modifiers = Modifiers::default();
        if !prefix.is_empty() {
            for token in prefix.split('+') {
                if token.trim().is_empty() {
                    return Err(format!("invalid key chord `{text}`"));
                }
                let modifier = Modifier::parse(token).ok_or_else(|| {
                    format!(
                        "`{}` is not a modifier; a chord holds modifiers and exactly one key",
                        token.trim()
                    )
                })?;
                modifiers.insert(modifier);
            }
        }

        let chord = if let Some(modifier) = Modifier::parse(key_name) {
            Chord {
                modifiers,
                key: modifier.keycode(),
                key_modifier: Some(modifier),
            }
        } else {
            let (key, shifted) =
                key_code(key_name).ok_or_else(|| format!("unknown key `{key_name}`"))?;
            if shifted {
                modifiers.insert(Modifier::Shift);
            }
            Chord {
                modifiers,
                key,
                key_modifier: None,
            }
        };

        if chord.key == KEY_ESCAPE
            && chord.modifiers.contains(Modifier::Cmd)
            && chord.modifiers.contains(Modifier::Alt)
        {
            return Err(if chord.modifiers.contains(Modifier::Ctrl) {
                "ctrl+alt+cmd+Escape is reserved for the computer-use emergency stop".into()
            } else {
                "cmd+alt+Escape (Force Quit) is reserved".into()
            });
        }
        Ok(chord)
    }
}

/// Returns the virtual key code and whether Shift is implied (shifted punctuation).
pub fn key_code(name: &str) -> Option<(u16, bool)> {
    let lower = name.to_ascii_lowercase();
    let code = match lower.as_str() {
        "return" | "enter" => KEY_RETURN,
        "tab" => KEY_TAB,
        "space" => 49,
        "backspace" => 51,
        "delete" => 117,
        "escape" | "esc" => KEY_ESCAPE,
        "up" | "arrowup" => 126,
        "down" | "arrowdown" => 125,
        "left" | "arrowleft" => 123,
        "right" | "arrowright" => 124,
        "home" => 115,
        "end" => 119,
        "page_up" | "pageup" | "prior" => 116,
        "page_down" | "pagedown" | "next" => 121,
        "capslock" | "caps_lock" => 57,
        "minus" => 27,
        "equal" => 24,
        "bracketleft" => 33,
        "bracketright" => 30,
        "semicolon" => 41,
        "apostrophe" => 39,
        "comma" => 43,
        "period" => 47,
        "slash" => 44,
        "backslash" => 42,
        "grave" => 50,
        "f1" => 122,
        "f2" => 120,
        "f3" => 99,
        "f4" => 118,
        "f5" => 96,
        "f6" => 97,
        "f7" => 98,
        "f8" => 100,
        "f9" => 101,
        "f10" => 109,
        "f11" => 103,
        "f12" => 111,
        "f13" => 105,
        "f14" => 107,
        "f15" => 113,
        "f16" => 106,
        "f17" => 64,
        "f18" => 79,
        "f19" => 80,
        "f20" => 90,
        _ => {
            let mut chars = lower.chars();
            let (Some(c), None) = (chars.next(), chars.next()) else {
                return None;
            };
            return char_code(c);
        }
    };
    Some((code, false))
}

fn char_code(c: char) -> Option<(u16, bool)> {
    let plain = |code| Some((code, false));
    let shifted = |code| Some((code, true));
    match c {
        'a' => plain(0),
        's' => plain(1),
        'd' => plain(2),
        'f' => plain(3),
        'h' => plain(4),
        'g' => plain(5),
        'z' => plain(6),
        'x' => plain(7),
        'c' => plain(8),
        'v' => plain(9),
        'b' => plain(11),
        'q' => plain(12),
        'w' => plain(13),
        'e' => plain(14),
        'r' => plain(15),
        'y' => plain(16),
        't' => plain(17),
        'o' => plain(31),
        'u' => plain(32),
        'i' => plain(34),
        'p' => plain(35),
        'l' => plain(37),
        'j' => plain(38),
        'k' => plain(40),
        'n' => plain(45),
        'm' => plain(46),
        '1' => plain(18),
        '2' => plain(19),
        '3' => plain(20),
        '4' => plain(21),
        '6' => plain(22),
        '5' => plain(23),
        '9' => plain(25),
        '7' => plain(26),
        '8' => plain(28),
        '0' => plain(29),
        '=' => plain(24),
        '-' => plain(27),
        ']' => plain(30),
        '[' => plain(33),
        '\'' => plain(39),
        ';' => plain(41),
        '\\' => plain(42),
        ',' => plain(43),
        '/' => plain(44),
        '.' => plain(47),
        '`' => plain(50),
        '!' => shifted(18),
        '@' => shifted(19),
        '#' => shifted(20),
        '$' => shifted(21),
        '^' => shifted(22),
        '%' => shifted(23),
        '+' => shifted(24),
        '(' => shifted(25),
        '&' => shifted(26),
        '_' => shifted(27),
        '*' => shifted(28),
        ')' => shifted(29),
        '}' => shifted(30),
        '{' => shifted(33),
        '"' => shifted(39),
        ':' => shifted(41),
        '|' => shifted(42),
        '<' => shifted(43),
        '?' => shifted(44),
        '>' => shifted(47),
        '~' => shifted(50),
        _ => None,
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TypeSegment {
    /// UTF-16 units for `CGEventKeyboardSetUnicodeString`, never splitting a surrogate pair.
    Text(Vec<u16>),
    /// A key tap (Return for newlines, Tab for tabs).
    Key(u16),
}

pub fn type_segments(text: &str) -> Vec<TypeSegment> {
    let mut segments = Vec::new();
    let mut chunk: Vec<u16> = Vec::new();
    let mut chars = text.chars().peekable();
    while let Some(c) = chars.next() {
        let key = match c {
            '\r' => {
                if chars.peek() == Some(&'\n') {
                    chars.next();
                }
                Some(KEY_RETURN)
            }
            '\n' => Some(KEY_RETURN),
            '\t' => Some(KEY_TAB),
            _ => None,
        };
        if let Some(key) = key {
            if !chunk.is_empty() {
                segments.push(TypeSegment::Text(std::mem::take(&mut chunk)));
            }
            segments.push(TypeSegment::Key(key));
            continue;
        }
        let mut units = [0u16; 2];
        let units = c.encode_utf16(&mut units);
        if chunk.len() + units.len() > TYPE_CHUNK_UNITS {
            segments.push(TypeSegment::Text(std::mem::take(&mut chunk)));
        }
        chunk.extend_from_slice(units);
    }
    if !chunk.is_empty() {
        segments.push(TypeSegment::Text(chunk));
    }
    segments
}

#[cfg(test)]
mod tests {
    use super::*;

    fn chord(text: &str) -> Chord {
        Chord::parse(text).unwrap_or_else(|e| panic!("{text}: {e}"))
    }

    #[test]
    fn parses_modifier_aliases() {
        for name in ["cmd", "Command", "SUPER", "meta", "win"] {
            assert_eq!(Modifier::parse(name), Some(Modifier::Cmd), "{name}");
        }
        for name in ["ctrl", "Control"] {
            assert_eq!(Modifier::parse(name), Some(Modifier::Ctrl), "{name}");
        }
        for name in ["alt", "option", "OPT"] {
            assert_eq!(Modifier::parse(name), Some(Modifier::Alt), "{name}");
        }
        assert_eq!(Modifier::parse("Shift"), Some(Modifier::Shift));
        assert_eq!(Modifier::parse("fn"), Some(Modifier::Fn));
        assert_eq!(Modifier::parse("hyper"), None);
    }

    #[test]
    fn parses_xdotool_and_openai_names() {
        let cases = [
            ("Return", 36),
            ("ENTER", 36),
            ("Tab", 48),
            ("space", 49),
            ("BackSpace", 51),
            ("Backspace", 51),
            ("Delete", 117),
            ("Escape", 53),
            ("ESC", 53),
            ("Up", 126),
            ("ArrowUp", 126),
            ("ARROWDOWN", 125),
            ("Left", 123),
            ("arrowright", 124),
            ("Home", 115),
            ("End", 119),
            ("Page_Up", 116),
            ("PageUp", 116),
            ("Prior", 116),
            ("Page_Down", 121),
            ("PAGEDOWN", 121),
            ("Next", 121),
            ("F1", 122),
            ("f12", 111),
            ("F20", 90),
            ("CapsLock", 57),
            ("a", 0),
            ("Z", 6),
            ("0", 29),
            ("9", 25),
            ("minus", 27),
            ("equal", 24),
            ("bracketleft", 33),
            ("bracketright", 30),
            ("semicolon", 41),
            ("apostrophe", 39),
            ("comma", 43),
            ("period", 47),
            ("slash", 44),
            ("backslash", 42),
            ("grave", 50),
            ("-", 27),
            ("/", 44),
            ("`", 50),
        ];
        for (name, code) in cases {
            assert_eq!(key_code(name), Some((code, false)), "{name}");
        }
        assert_eq!(key_code("?"), Some((44, true)));
        assert_eq!(key_code("F21"), None);
        assert_eq!(key_code("ab"), None);
        assert_eq!(key_code("é"), None);
    }

    #[test]
    fn parses_chords() {
        let c = chord("cmd+c");
        assert_eq!(c.key, 8);
        assert_eq!(c.modifiers.iter().collect::<Vec<_>>(), vec![Modifier::Cmd]);

        let c = chord("ctrl+shift+Tab");
        assert_eq!(c.key, KEY_TAB);
        assert_eq!(
            c.modifiers.iter().collect::<Vec<_>>(),
            vec![Modifier::Ctrl, Modifier::Shift]
        );

        let c = chord(" Return ");
        assert_eq!(c.key, KEY_RETURN);
        assert_eq!(c.modifiers, Modifiers::default());

        let c = chord("cmd+shift+?");
        assert_eq!(c.key, 44);
        assert!(c.modifiers.contains(Modifier::Shift));

        let c = chord("cmd++");
        assert_eq!(c.key, 24);
        assert!(c.modifiers.contains(Modifier::Cmd) && c.modifiers.contains(Modifier::Shift));
        assert_eq!(chord("+").key, 24);

        let c = chord("shift");
        assert_eq!(c.key, 56);
        assert_eq!(c.key_modifier, Some(Modifier::Shift));
        assert_eq!(c.modifiers, Modifiers::default());

        assert_eq!(chord("Cmd + Space").key, 49);
    }

    #[test]
    fn rejects_bad_chords() {
        for text in [
            "",
            "cmd+",
            "c+cmd",
            "cmd+foo",
            "cmd++c",
            "a+b",
            "ctrl+shift+F99",
        ] {
            assert!(Chord::parse(text).is_err(), "{text}");
        }
    }

    #[test]
    fn rejects_reserved_chords() {
        for text in [
            "ctrl+alt+cmd+Escape",
            "control+option+command+esc",
            "cmd+alt+Escape",
            "alt+cmd+ESC",
            "cmd+option+shift+Escape",
        ] {
            let error = Chord::parse(text).expect_err(text);
            assert!(error.contains("reserved"), "{text}: {error}");
        }
        assert!(Chord::parse("cmd+Escape").is_ok());
        assert!(Chord::parse("Escape").is_ok());
    }

    #[test]
    fn parses_held_modifiers() {
        let m = Modifiers::parse("cmd+shift").unwrap();
        assert!(m.contains(Modifier::Cmd) && m.contains(Modifier::Shift));
        assert_eq!(Modifiers::parse("").unwrap(), Modifiers::default());
        assert!(Modifiers::parse("shift+a").is_err());
    }

    #[test]
    fn segments_typed_text() {
        let units = |s: &str| s.encode_utf16().collect::<Vec<_>>();
        assert_eq!(
            type_segments("hi\nthere\r\nx\ty"),
            vec![
                TypeSegment::Text(units("hi")),
                TypeSegment::Key(KEY_RETURN),
                TypeSegment::Text(units("there")),
                TypeSegment::Key(KEY_RETURN),
                TypeSegment::Text(units("x")),
                TypeSegment::Key(KEY_TAB),
                TypeSegment::Text(units("y")),
            ]
        );
        assert_eq!(type_segments("\n\n").len(), 2);

        let long = "a".repeat(40);
        let segments = type_segments(&long);
        assert_eq!(segments.len(), 3);
        assert_eq!(segments[0], TypeSegment::Text(units(&"a".repeat(16))));
        assert_eq!(segments[2], TypeSegment::Text(units(&"a".repeat(8))));
    }

    #[test]
    fn never_splits_surrogate_pairs() {
        // 15 BMP units then an astral character (2 units): the pair must move whole.
        let text = format!("{}😀b", "a".repeat(15));
        let segments = type_segments(&text);
        let TypeSegment::Text(first) = &segments[0] else {
            panic!("expected text");
        };
        assert_eq!(first.len(), 15);
        let TypeSegment::Text(second) = &segments[1] else {
            panic!("expected text");
        };
        assert_eq!(second, &"😀b".encode_utf16().collect::<Vec<_>>());
        for segment in type_segments(&"😀".repeat(20)) {
            let TypeSegment::Text(units) = segment else {
                panic!("expected text");
            };
            assert!(units.len() <= TYPE_CHUNK_UNITS);
            assert!(String::from_utf16(&units).is_ok());
        }
    }
}
