// gratefulagents — OpenAI OAuth sign-in.
//
// Two supported flows:
//
//  * Browser (primary): PKCE authorization-code flow against
//    https://auth.openai.com with a loopback callback server. One click in the
//    browser completes sign-in; the
//    frontend polls `poll_openai_oauth` until tokens are exchanged.
//  * Device code (fallback): the provider device endpoint + one-time code,
//    polled until completed.
//
// Both produce the supported `auth.json` shape ({"tokens": {...}, "last_refresh"}),
// which the backend and SDK already parse and refresh. The PKCE verifier and
// state stay in this process; the frontend only ever sees the authorize URL,
// pending status, and the final credential JSON for UpdateMyCredentials.

use std::io::{Read, Write};
use std::net::TcpListener;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::oauth_common::{
    error_response, generate_pkce, http_client, jwt_claims, random_urlsafe, rfc3339_now,
    PENDING_TTL,
};

const CLIENT_ID: &str = "app_EMoamEEZ73f0CkXaXp7hrann";
const ISSUER: &str = "https://auth.openai.com";
const SCOPE: &str = "openid profile email offline_access";
// The loopback port; the redirect URI must match what the issuer allows.
const CALLBACK_PORT: u16 = 1455;
const CALLBACK_PATH: &str = "/auth/callback";
const USER_AGENT: &str = "gratefulagents";
const DEVICE_TTL: Duration = Duration::from_secs(15 * 60);

#[derive(Default)]
pub struct OpenAIOAuthState(Mutex<Option<Flow>>);

enum Flow {
    Browser(BrowserFlow),
    Device(DeviceFlow),
}

struct BrowserFlow {
    verifier: String,
    started: SystemTime,
    slot: Arc<Mutex<CallbackSlot>>,
    cancel: Arc<AtomicBool>,
}

#[derive(Default)]
struct CallbackSlot {
    code: Option<String>,
    error: Option<String>,
}

struct DeviceFlow {
    device_auth_id: String,
    user_code: String,
    started: SystemTime,
}

impl Flow {
    fn cancel(&self) {
        if let Flow::Browser(browser) = self {
            browser.cancel.store(true, Ordering::Relaxed);
        }
    }
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenAIOAuthStart {
    pub mode: String,
    pub authorize_url: Option<String>,
    pub user_code: Option<String>,
    pub verification_uri: Option<String>,
    pub interval: Option<u64>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenAIOAuthPollResult {
    pub status: String,
    pub email: Option<String>,
    pub account_id: Option<String>,
    pub openai_oauth_json: Option<String>,
    pub error: Option<String>,
}

impl OpenAIOAuthPollResult {
    fn pending() -> Self {
        Self {
            status: "pending".into(),
            email: None,
            account_id: None,
            openai_oauth_json: None,
            error: None,
        }
    }

    fn terminal(status: &str, error: Option<String>) -> Self {
        Self {
            status: status.into(),
            email: None,
            account_id: None,
            openai_oauth_json: None,
            error,
        }
    }
}

/// Starts the browser PKCE flow: binds localhost:1455, spawns the callback
/// listener, and returns the authorize URL for the frontend to open.
#[tauri::command]
pub fn start_openai_oauth(
    state: tauri::State<'_, OpenAIOAuthState>,
) -> Result<OpenAIOAuthStart, String> {
    let mut guard = state.0.lock().map_err(|_| "OAuth state poisoned".to_string())?;
    if let Some(existing) = guard.take() {
        existing.cancel();
    }

    let listener = TcpListener::bind(("127.0.0.1", CALLBACK_PORT)).map_err(|e| {
        format!(
            "Could not open localhost port {CALLBACK_PORT} for the sign-in callback ({e}). \
             Close any conflicting local sign-in and retry, or use the device-code option."
        )
    })?;
    listener
        .set_nonblocking(true)
        .map_err(|e| format!("configure callback listener: {e}"))?;

    let pkce = generate_pkce()?;
    let login_state = random_urlsafe(32)?;

    let mut url = url::Url::parse(ISSUER).map_err(|e| format!("build authorize URL: {e}"))?;
    url.set_path("/oauth/authorize");
    url.query_pairs_mut()
        .append_pair("response_type", "code")
        .append_pair("client_id", CLIENT_ID)
        .append_pair(
            "redirect_uri",
            &format!("http://localhost:{CALLBACK_PORT}{CALLBACK_PATH}"),
        )
        .append_pair("scope", SCOPE)
        .append_pair("code_challenge", &pkce.challenge)
        .append_pair("code_challenge_method", "S256")
        .append_pair("id_token_add_organizations", "true")
        .append_pair("codex_cli_simplified_flow", "true")
        .append_pair("state", &login_state)
        .append_pair("originator", "operator");

    let slot = Arc::new(Mutex::new(CallbackSlot::default()));
    let cancel = Arc::new(AtomicBool::new(false));
    {
        let slot = Arc::clone(&slot);
        let cancel = Arc::clone(&cancel);
        std::thread::spawn(move || run_callback_server(listener, login_state, slot, cancel));
    }

    *guard = Some(Flow::Browser(BrowserFlow {
        verifier: pkce.verifier,
        started: SystemTime::now(),
        slot,
        cancel,
    }));

    Ok(OpenAIOAuthStart {
        mode: "browser".into(),
        authorize_url: Some(url.to_string()),
        user_code: None,
        verification_uri: None,
        interval: None,
    })
}

/// Starts the device-code fallback flow (no local port required).
#[tauri::command]
pub async fn start_openai_device_oauth(
    state: tauri::State<'_, OpenAIOAuthState>,
) -> Result<OpenAIOAuthStart, String> {
    let client = http_client(USER_AGENT)?;
    let resp = client
        .post(format!("{ISSUER}/api/accounts/deviceauth/usercode"))
        .json(&json!({ "client_id": CLIENT_ID }))
        .send()
        .await
        .map_err(|e| format!("start ChatGPT device login: {e}"))?;
    if !resp.status().is_success() {
        return Err(error_response(resp, "ChatGPT device login").await);
    }

    #[derive(Deserialize)]
    struct UserCodeResponse {
        device_auth_id: String,
        #[serde(alias = "usercode")]
        user_code: String,
        #[serde(default)]
        interval: Option<Value>,
    }
    let body: UserCodeResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse ChatGPT device login response: {e}"))?;
    if body.device_auth_id.trim().is_empty() || body.user_code.trim().is_empty() {
        return Err("ChatGPT device login response was missing required fields".into());
    }
    // The interval arrives as a string; accept numbers too and clamp sanely.
    let interval = body
        .interval
        .as_ref()
        .and_then(|v| match v {
            Value::String(s) => s.trim().parse::<u64>().ok(),
            Value::Number(n) => n.as_u64(),
            _ => None,
        })
        .unwrap_or(5)
        .clamp(1, 30);

    let mut guard = state.0.lock().map_err(|_| "OAuth state poisoned".to_string())?;
    if let Some(existing) = guard.take() {
        existing.cancel();
    }
    *guard = Some(Flow::Device(DeviceFlow {
        device_auth_id: body.device_auth_id,
        user_code: body.user_code.clone(),
        started: SystemTime::now(),
    }));

    Ok(OpenAIOAuthStart {
        mode: "device".into(),
        authorize_url: None,
        user_code: Some(body.user_code),
        verification_uri: Some(format!("{ISSUER}/codex/device")),
        interval: Some(interval),
    })
}

/// Polls the in-flight flow (browser or device). Returns pending until the
/// user approves, then exchanges the code and returns the credential JSON.
#[tauri::command]
pub async fn poll_openai_oauth(
    state: tauri::State<'_, OpenAIOAuthState>,
) -> Result<OpenAIOAuthPollResult, String> {
    enum Action {
        Pending,
        Expired,
        Fail(String),
        ExchangeBrowser { code: String, verifier: String },
        PollDevice { device_auth_id: String, user_code: String },
    }

    let action = {
        let mut guard = state.0.lock().map_err(|_| "OAuth state poisoned".to_string())?;
        match guard.as_ref() {
            None => return Err("No ChatGPT sign-in in progress. Start again.".into()),
            Some(Flow::Browser(browser)) => {
                let mut slot = browser
                    .slot
                    .lock()
                    .map_err(|_| "OAuth state poisoned".to_string())?;
                if let Some(error) = slot.error.take() {
                    drop(slot);
                    guard.take().map(|f| f.cancel());
                    Action::Fail(error)
                } else if let Some(code) = slot.code.take() {
                    let verifier = browser.verifier.clone();
                    drop(slot);
                    // The listener already stopped after capturing the code;
                    // keep the flow entry until the exchange finishes.
                    Action::ExchangeBrowser { code, verifier }
                } else if browser.started.elapsed().unwrap_or_default() > PENDING_TTL {
                    drop(slot);
                    guard.take().map(|f| f.cancel());
                    Action::Expired
                } else {
                    Action::Pending
                }
            }
            Some(Flow::Device(device)) => {
                if device.started.elapsed().unwrap_or_default() > DEVICE_TTL {
                    guard.take();
                    Action::Expired
                } else {
                    Action::PollDevice {
                        device_auth_id: device.device_auth_id.clone(),
                        user_code: device.user_code.clone(),
                    }
                }
            }
        }
    };

    match action {
        Action::Pending => Ok(OpenAIOAuthPollResult::pending()),
        Action::Expired => Ok(OpenAIOAuthPollResult::terminal(
            "expired",
            Some("Sign-in expired. Start again.".into()),
        )),
        Action::Fail(error) => Ok(OpenAIOAuthPollResult::terminal("error", Some(error))),
        Action::ExchangeBrowser { code, verifier } => {
            let redirect_uri = format!("http://localhost:{CALLBACK_PORT}{CALLBACK_PATH}");
            let result = exchange_code(&code, &verifier, &redirect_uri).await;
            clear_flow(&state)?;
            result
        }
        Action::PollDevice {
            device_auth_id,
            user_code,
        } => {
            let client = http_client(USER_AGENT)?;
            let resp = client
                .post(format!("{ISSUER}/api/accounts/deviceauth/token"))
                .json(&json!({
                    "device_auth_id": device_auth_id,
                    "user_code": user_code,
                }))
                .send()
                .await
                .map_err(|e| format!("poll ChatGPT device login: {e}"))?;
            let status = resp.status();
            // Forbidden/NotFound mean "not approved yet" for this endpoint.
            if status == reqwest::StatusCode::FORBIDDEN
                || status == reqwest::StatusCode::NOT_FOUND
            {
                return Ok(OpenAIOAuthPollResult::pending());
            }
            if !status.is_success() {
                let error = error_response(resp, "ChatGPT device login poll").await;
                clear_flow(&state)?;
                return Ok(OpenAIOAuthPollResult::terminal("error", Some(error)));
            }

            #[derive(Deserialize)]
            struct CodeSuccessResponse {
                authorization_code: String,
                code_verifier: String,
            }
            let body: CodeSuccessResponse = resp
                .json()
                .await
                .map_err(|e| format!("parse ChatGPT device login poll response: {e}"))?;
            let result = exchange_code(
                &body.authorization_code,
                &body.code_verifier,
                &format!("{ISSUER}/deviceauth/callback"),
            )
            .await;
            clear_flow(&state)?;
            result
        }
    }
}

/// Cancels any in-flight sign-in and frees the callback port.
#[tauri::command]
pub fn cancel_openai_oauth(state: tauri::State<'_, OpenAIOAuthState>) -> Result<(), String> {
    clear_flow(&state)
}

fn clear_flow(state: &tauri::State<'_, OpenAIOAuthState>) -> Result<(), String> {
    if let Some(flow) = state
        .0
        .lock()
        .map_err(|_| "OAuth state poisoned".to_string())?
        .take()
    {
        flow.cancel();
    }
    Ok(())
}

/// Exchanges an authorization code for tokens and returns the completed poll
/// result carrying provider auth.json.
async fn exchange_code(
    code: &str,
    verifier: &str,
    redirect_uri: &str,
) -> Result<OpenAIOAuthPollResult, String> {
    let client = http_client(USER_AGENT)?;
    let resp = client
        .post(format!("{ISSUER}/oauth/token"))
        .form(&[
            ("grant_type", "authorization_code"),
            ("code", code),
            ("redirect_uri", redirect_uri),
            ("client_id", CLIENT_ID),
            ("code_verifier", verifier),
        ])
        .send()
        .await
        .map_err(|e| format!("exchange ChatGPT authorization code: {e}"))?;
    if !resp.status().is_success() {
        return Err(error_response(resp, "ChatGPT token exchange").await);
    }

    #[derive(Deserialize)]
    struct TokenResponse {
        id_token: String,
        access_token: String,
        refresh_token: String,
    }
    let tokens: TokenResponse = resp
        .json()
        .await
        .map_err(|e| format!("parse ChatGPT token response: {e}"))?;
    if tokens.access_token.trim().is_empty() || tokens.refresh_token.trim().is_empty() {
        return Err("ChatGPT token response was missing tokens".into());
    }

    let claims = jwt_claims(&tokens.id_token);
    let account_id = claims
        .as_ref()
        .and_then(|c| c["https://api.openai.com/auth"]["chatgpt_account_id"].as_str())
        .map(str::to_owned);
    let email = claims
        .as_ref()
        .and_then(|c| c["email"].as_str())
        .map(str::to_owned);

    let auth_json = build_auth_json(
        &tokens.id_token,
        &tokens.access_token,
        &tokens.refresh_token,
        account_id.as_deref(),
    )?;

    Ok(OpenAIOAuthPollResult {
        status: "completed".into(),
        email,
        account_id,
        openai_oauth_json: Some(auth_json),
        error: None,
    })
}

/// Serializes tokens in the supported auth.json shape so the backend and SDK
/// refresh logic apply unchanged.
fn build_auth_json(
    id_token: &str,
    access_token: &str,
    refresh_token: &str,
    account_id: Option<&str>,
) -> Result<String, String> {
    let mut tokens = serde_json::Map::new();
    tokens.insert("id_token".into(), Value::String(id_token.into()));
    tokens.insert("access_token".into(), Value::String(access_token.into()));
    tokens.insert("refresh_token".into(), Value::String(refresh_token.into()));
    if let Some(account_id) = account_id {
        tokens.insert("account_id".into(), Value::String(account_id.into()));
    }
    serde_json::to_string(&json!({
        "OPENAI_API_KEY": null,
        "tokens": Value::Object(tokens),
        "last_refresh": rfc3339_now(),
    }))
    .map_err(|e| format!("serialize ChatGPT credentials: {e}"))
}

/// Minimal single-purpose loopback HTTP server: waits for the OAuth redirect,
/// verifies state, records the code, answers the browser, and exits. Rejects
/// anything else. Never logs query parameters.
fn run_callback_server(
    listener: TcpListener,
    expected_state: String,
    slot: Arc<Mutex<CallbackSlot>>,
    cancel: Arc<AtomicBool>,
) {
    let deadline = SystemTime::now() + PENDING_TTL;
    loop {
        if cancel.load(Ordering::Relaxed) || SystemTime::now() > deadline {
            return;
        }
        let (mut stream, _) = match listener.accept() {
            Ok(conn) => conn,
            Err(err) if err.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(Duration::from_millis(150));
                continue;
            }
            Err(_) => return,
        };

        let _ = stream.set_read_timeout(Some(Duration::from_secs(3)));
        let request_target = read_request_target(&mut stream);
        let Some(target) = request_target else {
            respond(&mut stream, 400, "Bad request");
            continue;
        };

        match parse_callback(&target, &expected_state) {
            CallbackOutcome::NotCallback => {
                respond(&mut stream, 404, "Not found");
            }
            CallbackOutcome::StateMismatch => {
                respond(&mut stream, 400, "State mismatch");
            }
            CallbackOutcome::ProviderError(error) => {
                if let Ok(mut slot) = slot.lock() {
                    slot.error = Some(error);
                }
                respond_html(&mut stream, "Sign-in was not completed. You can close this tab and try again in gratefulagents.");
                return;
            }
            CallbackOutcome::Code(code) => {
                if let Ok(mut slot) = slot.lock() {
                    slot.code = Some(code);
                }
                respond_html(&mut stream, "Signed in to ChatGPT. You can close this tab and return to gratefulagents.");
                return;
            }
        }
    }
}

enum CallbackOutcome {
    NotCallback,
    StateMismatch,
    ProviderError(String),
    Code(String),
}

fn parse_callback(target: &str, expected_state: &str) -> CallbackOutcome {
    let Ok(url) = url::Url::parse(&format!("http://localhost{target}")) else {
        return CallbackOutcome::NotCallback;
    };
    if url.path() != CALLBACK_PATH {
        return CallbackOutcome::NotCallback;
    }
    let mut code = None;
    let mut state = None;
    let mut error = None;
    for (key, value) in url.query_pairs() {
        match key.as_ref() {
            "code" => code = Some(value.into_owned()),
            "state" => state = Some(value.into_owned()),
            "error" => error = Some(value.into_owned()),
            _ => {}
        }
    }
    if let Some(error) = error {
        return CallbackOutcome::ProviderError(format!("ChatGPT sign-in failed: {error}"));
    }
    if state.as_deref() != Some(expected_state) {
        return CallbackOutcome::StateMismatch;
    }
    match code {
        Some(code) if !code.trim().is_empty() => CallbackOutcome::Code(code),
        _ => CallbackOutcome::ProviderError("ChatGPT sign-in returned no code".into()),
    }
}

/// Reads just enough of the HTTP request to extract the request target from
/// the first line (e.g. "GET /auth/callback?... HTTP/1.1").
fn read_request_target(stream: &mut std::net::TcpStream) -> Option<String> {
    let mut buf = Vec::with_capacity(2048);
    let mut chunk = [0u8; 1024];
    while !buf.windows(2).any(|w| w == b"\r\n") && buf.len() < 16 * 1024 {
        match stream.read(&mut chunk) {
            Ok(0) => break,
            Ok(n) => buf.extend_from_slice(&chunk[..n]),
            Err(_) => break,
        }
    }
    let line_end = buf.windows(2).position(|w| w == b"\r\n")?;
    let line = std::str::from_utf8(&buf[..line_end]).ok()?;
    let mut parts = line.split_whitespace();
    let method = parts.next()?;
    let target = parts.next()?;
    if method != "GET" {
        return None;
    }
    Some(target.to_string())
}

fn respond(stream: &mut std::net::TcpStream, status: u16, message: &str) {
    let reason = match status {
        400 => "Bad Request",
        404 => "Not Found",
        _ => "OK",
    };
    let body = format!("{message}\n");
    let _ = stream.write_all(
        format!(
            "HTTP/1.1 {status} {reason}\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        )
        .as_bytes(),
    );
    let _ = stream.flush();
}

fn respond_html(stream: &mut std::net::TcpStream, message: &str) {
    let body = format!(
        "<!doctype html><html><head><meta charset=\"utf-8\"><title>gratefulagents</title></head>\
         <body style=\"font-family:-apple-system,system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#0b0b0f;color:#e7e7ea\">\
         <p style=\"font-size:15px\">{message}</p></body></html>"
    );
    let _ = stream.write_all(
        format!(
            "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        )
        .as_bytes(),
    );
    let _ = stream.flush();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_callback_accepts_matching_state() {
        match parse_callback("/auth/callback?code=c1&state=s1", "s1") {
            CallbackOutcome::Code(code) => assert_eq!(code, "c1"),
            _ => panic!("expected code"),
        }
    }

    #[test]
    fn parse_callback_rejects_state_mismatch_and_other_paths() {
        assert!(matches!(
            parse_callback("/auth/callback?code=c1&state=other", "s1"),
            CallbackOutcome::StateMismatch
        ));
        assert!(matches!(
            parse_callback("/favicon.ico", "s1"),
            CallbackOutcome::NotCallback
        ));
        assert!(matches!(
            parse_callback("/auth/callback?error=access_denied&state=s1", "s1"),
            CallbackOutcome::ProviderError(_)
        ));
    }

    #[test]
    fn auth_json_matches_codex_shape() {
        let raw = build_auth_json("idt", "at", "rt", Some("acct-1")).unwrap();
        let value: Value = serde_json::from_str(&raw).unwrap();
        assert!(value["OPENAI_API_KEY"].is_null());
        assert_eq!(value["tokens"]["id_token"], "idt");
        assert_eq!(value["tokens"]["access_token"], "at");
        assert_eq!(value["tokens"]["refresh_token"], "rt");
        assert_eq!(value["tokens"]["account_id"], "acct-1");
        assert!(value["last_refresh"].as_str().unwrap().ends_with('Z'));
    }
}
