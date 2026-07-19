// gratefulagents — Anthropic (Claude Pro/Max) OAuth sign-in.
//
// Implements a PKCE + manual-code flow: we open
// https://claude.ai/oauth/authorize in the browser with a locally generated
// S256 challenge; after approval Anthropic's callback page displays a
// "code#state" string the user pastes back into the app; we then exchange it
// at platform.claude.com for tokens. The result uses the expected
// `.credentials.json` shape (`claudeAiOauth`), which the backend and SDK
// already understand and can refresh (see gratefulagents-sdk providers/oauth).
//
// The PKCE verifier and state never leave this process; commands hand the
// frontend only the authorize URL and the final credential JSON to store via
// UpdateMyCredentials.

use std::sync::Mutex;
use std::time::SystemTime;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::oauth_common::{
    error_response, generate_pkce, http_client, random_urlsafe, unix_now, PENDING_TTL,
};

// These constants and the SDK refresh configuration keep issued tokens
// refreshable server-side.
const CLIENT_ID: &str = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
const AUTHORIZE_URL: &str = "https://claude.ai/oauth/authorize";
const REDIRECT_URI: &str = "https://platform.claude.com/oauth/code/callback";
const TOKEN_URL: &str = "https://platform.claude.com/v1/oauth/token";
const SCOPE: &str =
    "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload";
const USER_AGENT: &str = "gratefulagents";

#[derive(Default)]
pub struct AnthropicOAuthState(Mutex<Option<PendingLogin>>);

struct PendingLogin {
    verifier: String,
    state: String,
    started: SystemTime,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AnthropicOAuthStart {
    pub authorize_url: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AnthropicOAuthCompleteRequest {
    pub code: String,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AnthropicOAuthComplete {
    pub status: String,
    pub email: Option<String>,
    pub anthropic_oauth_json: String,
}

#[derive(Debug, Deserialize)]
struct TokenResponse {
    access_token: Option<String>,
    refresh_token: Option<String>,
    expires_in: Option<i64>,
    scope: Option<String>,
    account: Option<TokenAccount>,
}

#[derive(Debug, Deserialize)]
struct TokenAccount {
    uuid: Option<String>,
    email_address: Option<String>,
}

#[tauri::command]
pub fn start_anthropic_oauth(
    state: tauri::State<'_, AnthropicOAuthState>,
) -> Result<AnthropicOAuthStart, String> {
    let pkce = generate_pkce()?;
    let login_state = random_urlsafe(32)?;

    let mut url = url::Url::parse(AUTHORIZE_URL).map_err(|e| format!("build authorize URL: {e}"))?;
    url.query_pairs_mut()
        .append_pair("code", "true")
        .append_pair("client_id", CLIENT_ID)
        .append_pair("response_type", "code")
        .append_pair("redirect_uri", REDIRECT_URI)
        .append_pair("scope", SCOPE)
        .append_pair("code_challenge", &pkce.challenge)
        .append_pair("code_challenge_method", "S256")
        .append_pair("state", &login_state);

    *state.0.lock().map_err(|_| "OAuth state poisoned".to_string())? = Some(PendingLogin {
        verifier: pkce.verifier,
        state: login_state,
        started: SystemTime::now(),
    });

    Ok(AnthropicOAuthStart {
        authorize_url: url.to_string(),
    })
}

#[tauri::command]
pub async fn complete_anthropic_oauth(
    state: tauri::State<'_, AnthropicOAuthState>,
    payload: AnthropicOAuthCompleteRequest,
) -> Result<AnthropicOAuthComplete, String> {
    // Take the pending login so the single-use verifier cannot be replayed.
    let pending = state
        .0
        .lock()
        .map_err(|_| "OAuth state poisoned".to_string())?
        .take()
        .ok_or_else(|| "No Claude sign-in in progress. Start again.".to_string())?;
    if pending.started.elapsed().unwrap_or_default() > PENDING_TTL {
        return Err("Claude sign-in expired. Start again.".into());
    }

    let (code, returned_state) = parse_pasted_code(&payload.code)?;
    if let Some(returned) = &returned_state {
        if *returned != pending.state {
            return Err("Pasted code does not match this sign-in attempt. Start again.".into());
        }
    }

    let client = http_client(USER_AGENT)?;
    let resp = client
        .post(TOKEN_URL)
        .json(&json!({
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": REDIRECT_URI,
            "client_id": CLIENT_ID,
            "code_verifier": pending.verifier,
            "state": returned_state.unwrap_or(pending.state),
        }))
        .send()
        .await
        .map_err(|e| format!("exchange Claude authorization code: {e}"))?;
    if !resp.status().is_success() {
        return Err(error_response(resp, "Claude token exchange").await);
    }
    let token: TokenResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse Claude token response: {e}"))?;

    let access_token = token
        .access_token
        .as_deref()
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "Claude token response was missing access_token".to_string())?;
    let refresh_token = token
        .refresh_token
        .as_deref()
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .ok_or_else(|| "Claude token response was missing refresh_token".to_string())?;

    let email = token
        .account
        .as_ref()
        .and_then(|a| a.email_address.as_deref())
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_owned);
    let account_uuid = token
        .account
        .as_ref()
        .and_then(|a| a.uuid.as_deref())
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_owned);

    let auth_json = build_credentials_json(
        access_token,
        refresh_token,
        token.expires_in,
        token.scope.as_deref(),
        email.as_deref(),
        account_uuid.as_deref(),
    )?;

    Ok(AnthropicOAuthComplete {
        status: "completed".into(),
        email,
        anthropic_oauth_json: auth_json,
    })
}

/// Parses the value pasted from Anthropic's callback page. The page shows
/// `code#state`; be forgiving about surrounding whitespace/quotes and accept a
/// bare code (older pages) by falling back to our stored state.
fn parse_pasted_code(raw: &str) -> Result<(String, Option<String>), String> {
    let trimmed = raw.trim().trim_matches('"').trim();
    if trimmed.is_empty() {
        return Err("Paste the code shown after approving access".into());
    }
    match trimmed.split_once('#') {
        Some((code, state)) => {
            let code = code.trim();
            let state = state.trim();
            if code.is_empty() {
                return Err("Pasted value is missing the authorization code".into());
            }
            if state.is_empty() {
                Ok((code.to_string(), None))
            } else {
                Ok((code.to_string(), Some(state.to_string())))
            }
        }
        None => Ok((trimmed.to_string(), None)),
    }
}

/// Serializes credentials in the supported `.credentials.json` shape so
/// backend and SDK parsing are shared.
fn build_credentials_json(
    access_token: &str,
    refresh_token: &str,
    expires_in: Option<i64>,
    scope: Option<&str>,
    email: Option<&str>,
    account_uuid: Option<&str>,
) -> Result<String, String> {
    let scopes: Vec<&str> = scope
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .unwrap_or(SCOPE)
        .split_whitespace()
        .collect();

    let mut claude = serde_json::Map::new();
    claude.insert("accessToken".into(), Value::String(access_token.into()));
    claude.insert("refreshToken".into(), Value::String(refresh_token.into()));
    claude.insert("scopes".into(), json!(scopes));
    if let Some(expires_in) = expires_in {
        if expires_in > 0 {
            let expires_at_ms = (unix_now() + expires_in) * 1000;
            claude.insert("expiresAt".into(), json!(expires_at_ms));
        }
    }
    let mut token_account = serde_json::Map::new();
    if let Some(email) = email {
        token_account.insert("emailAddress".into(), Value::String(email.into()));
    }
    if let Some(uuid) = account_uuid {
        token_account.insert("uuid".into(), Value::String(uuid.into()));
    }
    if !token_account.is_empty() {
        claude.insert("tokenAccount".into(), Value::Object(token_account));
    }

    serde_json::to_string(&json!({ "claudeAiOauth": Value::Object(claude) }))
        .map_err(|e| format!("serialize Claude credentials: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_pasted_code_splits_code_and_state() {
        assert_eq!(
            parse_pasted_code("abc123#st_1").unwrap(),
            ("abc123".to_string(), Some("st_1".to_string()))
        );
        assert_eq!(
            parse_pasted_code("  \"abc123#st_1\"  ").unwrap(),
            ("abc123".to_string(), Some("st_1".to_string()))
        );
        assert_eq!(parse_pasted_code("abc123").unwrap(), ("abc123".to_string(), None));
        assert!(parse_pasted_code("   ").is_err());
        assert!(parse_pasted_code("#state-only").is_err());
    }

    #[test]
    fn credentials_json_uses_claude_code_shape() {
        let raw = build_credentials_json(
            "at-1",
            "rt-1",
            Some(3600),
            None,
            Some("a@b.c"),
            Some("uuid-1"),
        )
        .unwrap();
        let value: Value = serde_json::from_str(&raw).unwrap();
        let oauth = &value["claudeAiOauth"];
        assert_eq!(oauth["accessToken"], "at-1");
        assert_eq!(oauth["refreshToken"], "rt-1");
        assert_eq!(oauth["tokenAccount"]["emailAddress"], "a@b.c");
        assert_eq!(oauth["tokenAccount"]["uuid"], "uuid-1");
        assert!(oauth["expiresAt"].as_i64().unwrap() > unix_now() * 1000);
        assert_eq!(
            oauth["scopes"].as_array().unwrap().len(),
            SCOPE.split_whitespace().count()
        );
    }
}
