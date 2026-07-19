// gratefulagents — local credential detection (desktop).
//
// Provider OAuth credentials may already be present on disk. We read them so
// the app can offer a one-click import into the user's private chat credentials
// — letting them log in and start working immediately without pasting keys.
//
// Files are returned verbatim as `auth_json`; the backend validates and
// normalizes them before storage. The command is desktop-only and best-effort:
// any unreadable or malformed file is silently skipped so detection never
// blocks the UI.

use std::path::{Path, PathBuf};

use serde::Serialize;

// Credential files are tiny; cap reads so a stray large file can never be slurped
// into memory or shipped to the backend.
const MAX_CRED_BYTES: u64 = 256 * 1024;

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LocalCredential {
    /// Provider id understood by the backend: "openai" | "anthropic" | "copilot".
    pub provider: String,
    /// Short human label for the credential source.
    pub label: String,
    /// Absolute path the credential was read from.
    pub source_path: String,
    /// Best-effort account identifier (email or login); may be empty.
    pub account: Option<String>,
    /// Raw credential file contents, forwarded to the backend as `auth.json`.
    pub auth_json: String,
}

/// Detects provider credentials from local files. Returns an empty list when
/// nothing is found (including on mobile, where these paths do not exist).
#[tauri::command]
pub fn detect_local_credentials() -> Vec<LocalCredential> {
    let mut out = Vec::new();
    if let Some(c) = detect_openai() {
        out.push(c);
    }
    if let Some(c) = detect_anthropic() {
        out.push(c);
    }
    if let Some(c) = detect_copilot() {
        out.push(c);
    }
    out
}

fn home_dir() -> Option<PathBuf> {
    if let Some(home) = std::env::var_os("HOME") {
        if !home.is_empty() {
            return Some(PathBuf::from(home));
        }
    }
    // Windows fallback.
    std::env::var_os("USERPROFILE")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
}

fn config_dir() -> Option<PathBuf> {
    if let Some(xdg) = std::env::var_os("XDG_CONFIG_HOME") {
        if !xdg.is_empty() {
            return Some(PathBuf::from(xdg));
        }
    }
    home_dir().map(|h| h.join(".config"))
}

// read_credential reads a small JSON credential file, returning its contents when
// it exists, is within the size cap, and looks like a JSON object.
fn read_credential(path: &Path) -> Option<String> {
    let meta = std::fs::metadata(path).ok()?;
    if !meta.is_file() || meta.len() == 0 || meta.len() > MAX_CRED_BYTES {
        return None;
    }
    let contents = std::fs::read_to_string(path).ok()?;
    if !contents.trim_start().starts_with('{') {
        return None;
    }
    Some(contents)
}

fn parse_json(raw: &str) -> Option<serde_json::Value> {
    serde_json::from_str(raw).ok()
}

fn detect_openai() -> Option<LocalCredential> {
    // Honor the configured location, then use the default location.
    let base = std::env::var_os("CODEX_HOME")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .or_else(|| home_dir().map(|h| h.join(".codex")))?;
    let path = base.join("auth.json");
    let raw = read_credential(&path)?;

    // An account id may live under tokens.
    let account = parse_json(&raw).and_then(|v| {
        non_empty(v.get("tokens").and_then(|t| t.get("account_id")).and_then(|s| s.as_str()))
            .or_else(|| non_empty(v.get("account_id").and_then(|s| s.as_str())))
    });

    Some(LocalCredential {
        provider: "openai".into(),
        label: "OpenAI OAuth".into(),
        source_path: path.to_string_lossy().into_owned(),
        account,
        auth_json: raw,
    })
}

fn detect_anthropic() -> Option<LocalCredential> {
    let home = home_dir()?;
    let candidates = [
        home.join(".claude").join(".credentials.json"),
        home.join(".config").join("claude").join(".credentials.json"),
    ];
    let path = candidates.into_iter().find(|p| p.is_file())?;
    let raw = read_credential(&path)?;

    // The OAuth payload may include an account email.
    let account = parse_json(&raw).and_then(|v| {
        let oauth = v.get("claudeAiOauth");
        non_empty(
            oauth
                .and_then(|o| o.get("tokenAccount"))
                .and_then(|a| a.get("emailAddress"))
                .and_then(|s| s.as_str()),
        )
        .or_else(|| non_empty(v.get("email").and_then(|s| s.as_str())))
    });

    Some(LocalCredential {
        provider: "anthropic".into(),
        label: "Anthropic OAuth".into(),
        source_path: path.to_string_lossy().into_owned(),
        account,
        auth_json: raw,
    })
}

fn detect_copilot() -> Option<LocalCredential> {
    let base = config_dir()?.join("github-copilot");
    // Prefer the first supported credential file present.
    let path = ["apps.json", "hosts.json"]
        .iter()
        .map(|f| base.join(f))
        .find(|p| p.is_file())?;
    let raw = read_credential(&path)?;

    // The supported files are host-keyed and may include a user login.
    let account = parse_json(&raw).and_then(|v| {
        let obj = v.as_object()?;
        for entry in obj.values() {
            if let Some(user) = non_empty(entry.get("user").and_then(|s| s.as_str())) {
                return Some(user);
            }
        }
        None
    });

    Some(LocalCredential {
        provider: "copilot".into(),
        label: "GitHub OAuth".into(),
        source_path: path.to_string_lossy().into_owned(),
        account,
        auth_json: raw,
    })
}

fn non_empty(value: Option<&str>) -> Option<String> {
    value
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
}
