// gratefulagents — shared helpers for provider OAuth flows (Anthropic, OpenAI).
//
// Small, dependency-light primitives used by the browser/paste-code OAuth
// commands: PKCE generation, URL-safe randomness, JWT claim extraction, and
// HTTP error sanitization that never leaks tokens into logs or the UI.

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use serde_json::Value;
use sha2::{Digest, Sha256};

pub const REQUEST_TIMEOUT: Duration = Duration::from_secs(20);
/// How long a started login stays valid before the user must start over.
pub const PENDING_TTL: Duration = Duration::from_secs(10 * 60);

pub struct Pkce {
    pub verifier: String,
    pub challenge: String,
}

pub fn generate_pkce() -> Result<Pkce, String> {
    let verifier = random_urlsafe(64)?;
    let digest = Sha256::digest(verifier.as_bytes());
    Ok(Pkce {
        challenge: URL_SAFE_NO_PAD.encode(digest),
        verifier,
    })
}

pub fn random_urlsafe(bytes: usize) -> Result<String, String> {
    let mut buf = vec![0u8; bytes];
    getrandom::fill(&mut buf).map_err(|e| format!("generate randomness: {e}"))?;
    Ok(URL_SAFE_NO_PAD.encode(buf))
}

pub fn unix_now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

pub fn rfc3339_now() -> String {
    humantime::format_rfc3339_seconds(SystemTime::now()).to_string()
}

/// Decodes a JWT (without verification — we only display/derive identifiers
/// from tokens we just received over TLS from the issuer) and returns its
/// payload claims.
pub fn jwt_claims(token: &str) -> Option<Value> {
    let payload = token.split('.').nth(1)?;
    let raw = URL_SAFE_NO_PAD.decode(payload.trim_end_matches('=')).ok()?;
    serde_json::from_slice(&raw).ok()
}

pub fn http_client(user_agent: &str) -> Result<reqwest::Client, String> {
    reqwest::Client::builder()
        .timeout(REQUEST_TIMEOUT)
        .user_agent(user_agent.to_string())
        .build()
        .map_err(|e| format!("build HTTP client: {e}"))
}

pub async fn error_response(resp: reqwest::Response, context: &str) -> String {
    let status = resp.status();
    let body = resp.text().await.unwrap_or_default();
    let sanitized = sanitize_error_body(&body);
    if sanitized.trim().is_empty() {
        format!("{context} failed with status {status}")
    } else {
        format!("{context} failed with status {status}: {sanitized}")
    }
}

fn sanitize_error_body(raw: &str) -> String {
    let limited = raw.chars().take(4096).collect::<String>();
    if let Ok(mut value) = serde_json::from_str::<Value>(&limited) {
        redact_json_tokens(&mut value);
        if let Ok(out) = serde_json::to_string(&value) {
            return out;
        }
    }
    limited
}

fn redact_json_tokens(value: &mut Value) {
    match value {
        Value::Object(map) => {
            for (key, child) in map.iter_mut() {
                let lower = key.to_ascii_lowercase();
                if lower.contains("token") || lower == "authorization" {
                    *child = Value::String("[redacted]".into());
                } else {
                    redact_json_tokens(child);
                }
            }
        }
        Value::Array(values) => {
            for child in values {
                redact_json_tokens(child);
            }
        }
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pkce_challenge_is_s256_of_verifier() {
        let pkce = generate_pkce().expect("pkce");
        let digest = Sha256::digest(pkce.verifier.as_bytes());
        assert_eq!(pkce.challenge, URL_SAFE_NO_PAD.encode(digest));
        // 64 random bytes -> 86 base64url chars, within RFC 7636's 43-128.
        assert_eq!(pkce.verifier.len(), 86);
    }

    #[test]
    fn jwt_claims_decodes_payload() {
        let payload = URL_SAFE_NO_PAD.encode(r#"{"email":"a@b.c","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}"#);
        let token = format!("eyJhbGciOiJIUzI1NiJ9.{payload}.sig");
        let claims = jwt_claims(&token).expect("claims");
        assert_eq!(claims["email"], "a@b.c");
        assert_eq!(
            claims["https://api.openai.com/auth"]["chatgpt_account_id"],
            "acct-1"
        );
    }
}
