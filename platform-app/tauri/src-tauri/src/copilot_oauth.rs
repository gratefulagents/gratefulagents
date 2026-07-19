use std::time::{Duration, SystemTime, UNIX_EPOCH};

use reqwest::header::{ACCEPT, AUTHORIZATION, CONTENT_TYPE, USER_AGENT};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

const GITHUB_CLIENT_ID: &str = "Iv1.b507a08c87ecfe98";
const GITHUB_DEVICE_URL: &str = "https://github.com/login/device/code";
const GITHUB_TOKEN_URL: &str = "https://github.com/login/oauth/access_token";
const GITHUB_USER_URL: &str = "https://api.github.com/user";
const COPILOT_TOKEN_URL: &str = "https://api.github.com/copilot_internal/v2/token";
const REQUEST_TIMEOUT: Duration = Duration::from_secs(20);

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CopilotOAuthStart {
    pub device_code: String,
    pub user_code: String,
    pub verification_uri: String,
    pub verification_uri_complete: Option<String>,
    pub expires_at: i64,
    pub interval: u64,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CopilotOAuthPollRequest {
    pub device_code: String,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CopilotOAuthPollResult {
    pub status: String,
    pub interval: Option<u64>,
    pub login: Option<String>,
    pub copilot_oauth_json: Option<String>,
    pub error: Option<String>,
}

#[derive(Debug, Deserialize)]
struct DeviceCodeResponse {
    device_code: String,
    user_code: String,
    verification_uri: String,
    verification_uri_complete: Option<String>,
    expires_in: i64,
    interval: Option<u64>,
    error: Option<String>,
    error_description: Option<String>,
}

#[derive(Debug, Deserialize)]
struct TokenResponse {
    access_token: Option<String>,
    error: Option<String>,
    error_description: Option<String>,
    interval: Option<u64>,
}

#[derive(Debug, Deserialize)]
struct CopilotTokenResponse {
    token: Option<String>,
    expires_at: Option<i64>,
}

#[derive(Debug, Deserialize)]
struct GitHubUserResponse {
    login: Option<String>,
}

#[tauri::command]
pub async fn start_copilot_oauth() -> Result<CopilotOAuthStart, String> {
    let client = http_client()?;
    let resp = client
        .post(GITHUB_DEVICE_URL)
        .header(ACCEPT, "application/json")
        .header(CONTENT_TYPE, "application/json")
        .json(&json!({
            "client_id": GITHUB_CLIENT_ID,
            "scope": "read:user",
        }))
        .send()
        .await
        .map_err(|e| format!("start GitHub device login: {e}"))?;

    if !resp.status().is_success() {
        return Err(error_response(resp, "GitHub device login").await);
    }

    let body: DeviceCodeResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse GitHub device login response: {e}"))?;
    if let Some(err) = body.error {
        return Err(body.error_description.unwrap_or(err));
    }
    if body.device_code.trim().is_empty()
        || body.user_code.trim().is_empty()
        || body.verification_uri.trim().is_empty()
    {
        return Err("GitHub device login response was missing required fields".into());
    }

    Ok(CopilotOAuthStart {
        device_code: body.device_code,
        user_code: body.user_code,
        verification_uri: body.verification_uri,
        verification_uri_complete: body.verification_uri_complete,
        expires_at: unix_now() + body.expires_in.max(0),
        interval: body.interval.unwrap_or(5).max(5),
    })
}

#[tauri::command]
pub async fn poll_copilot_oauth(
    payload: CopilotOAuthPollRequest,
) -> Result<CopilotOAuthPollResult, String> {
    let device_code = payload.device_code.trim();
    if device_code.is_empty() {
        return Err("device code is required".into());
    }

    let client = http_client()?;
    let resp = client
        .post(GITHUB_TOKEN_URL)
        .header(ACCEPT, "application/json")
        .header(CONTENT_TYPE, "application/json")
        .json(&json!({
            "client_id": GITHUB_CLIENT_ID,
            "device_code": device_code,
            "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
        }))
        .send()
        .await
        .map_err(|e| format!("poll GitHub device login: {e}"))?;

    if !resp.status().is_success() {
        return Err(error_response(resp, "GitHub device login poll").await);
    }

    let token_resp: TokenResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse GitHub device login poll response: {e}"))?;
    if let Some(err) = token_resp.error.as_deref() {
        return Ok(match err {
            "authorization_pending" => CopilotOAuthPollResult::pending(token_resp.interval),
            "slow_down" => {
                CopilotOAuthPollResult::pending(Some(token_resp.interval.unwrap_or(10).max(10)))
            }
            "expired_token" => {
                CopilotOAuthPollResult::terminal("expired", token_resp.error_description)
            }
            "access_denied" => CopilotOAuthPollResult::terminal(
                "denied",
                token_resp.error_description,
            ),
            _ => CopilotOAuthPollResult::terminal(
                "error",
                token_resp.error_description.or_else(|| Some(err.to_string())),
            ),
        });
    }

    let github_token = token_resp
        .access_token
        .filter(|s| !s.trim().is_empty())
        .ok_or_else(|| "GitHub device login completed without an access token".to_string())?;

    let copilot = fetch_copilot_token(&client, &github_token).await?;
    let login = fetch_github_login(&client, &github_token).await.ok().flatten();

    let mut auth_json = Map::new();
    auth_json.insert("oauth_token".into(), Value::String(github_token));
    auth_json.insert("token".into(), Value::String(copilot.token));
    auth_json.insert("type".into(), Value::String("copilot".into()));
    if let Some(expires_at) = copilot.expires_at {
        if expires_at > 0 {
            auth_json.insert("expires_at".into(), Value::Number(expires_at.into()));
        }
    }

    let copilot_oauth_json = serde_json::to_string(&Value::Object(auth_json))
        .map_err(|e| format!("serialize Copilot credentials: {e}"))?;

    Ok(CopilotOAuthPollResult {
        status: "completed".into(),
        interval: None,
        login,
        copilot_oauth_json: Some(copilot_oauth_json),
        error: None,
    })
}

impl CopilotOAuthPollResult {
    fn pending(interval: Option<u64>) -> Self {
        Self {
            status: "pending".into(),
            interval,
            login: None,
            copilot_oauth_json: None,
            error: None,
        }
    }

    fn terminal(status: &str, error: Option<String>) -> Self {
        Self {
            status: status.into(),
            interval: None,
            login: None,
            copilot_oauth_json: None,
            error,
        }
    }
}

struct CopilotToken {
    token: String,
    expires_at: Option<i64>,
}

async fn fetch_copilot_token(
    client: &reqwest::Client,
    github_token: &str,
) -> Result<CopilotToken, String> {
    let resp = client
        .get(COPILOT_TOKEN_URL)
        .header(AUTHORIZATION, format!("token {github_token}"))
        .header(ACCEPT, "application/json")
        .header("Editor-Plugin-Version", "gratefulagents/unknown")
        .header(USER_AGENT, "gratefulagents")
        .send()
        .await
        .map_err(|e| format!("request Copilot API token: {e}"))?;

    if !resp.status().is_success() {
        return Err(error_response(resp, "Copilot API token request").await);
    }

    let body: CopilotTokenResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse Copilot API token response: {e}"))?;
    let token = body
        .token
        .filter(|s| !s.trim().is_empty())
        .ok_or_else(|| "Copilot API token response was missing token".to_string())?;

    Ok(CopilotToken {
        token,
        expires_at: body.expires_at,
    })
}

async fn fetch_github_login(
    client: &reqwest::Client,
    github_token: &str,
) -> Result<Option<String>, String> {
    let resp = client
        .get(GITHUB_USER_URL)
        .header(AUTHORIZATION, format!("token {github_token}"))
        .header(ACCEPT, "application/json")
        .header(USER_AGENT, "gratefulagents")
        .send()
        .await
        .map_err(|e| format!("request GitHub user: {e}"))?;
    if !resp.status().is_success() {
        return Ok(None);
    }
    let body: GitHubUserResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse GitHub user response: {e}"))?;
    Ok(body.login.filter(|s| !s.trim().is_empty()))
}

fn http_client() -> Result<reqwest::Client, String> {
    reqwest::Client::builder()
        .timeout(REQUEST_TIMEOUT)
        .user_agent("gratefulagents")
        .build()
        .map_err(|e| format!("build HTTP client: {e}"))
}

async fn error_response(resp: reqwest::Response, context: &str) -> String {
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

fn unix_now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}
