// Desktop Google OAuth via the system default browser (RFC 8252).
//
// Embedded webviews (WKWebView / WebKitGTK / WebView2) do not expose the
// WebAuthn APIs, so Google accounts protected by passkeys cannot complete
// sign-in inside an in-app window. Instead we open the user's real browser —
// which fully supports passkeys — and receive the OAuth redirect on a
// loopback HTTP server.
//
// Because the flow uses `response_type=id_token`, Google returns the token in
// the URL *fragment*, which browsers never send to a server. The loopback
// server therefore serves a tiny page at `/callback` that forwards
// `location.hash` to `/finish?...` where the token is actually parsed.

use std::sync::Mutex;
use std::time::Duration;

use serde::de::DeserializeOwned;
use tauri::{plugin::PluginApi, AppHandle, Manager, Runtime};
use tauri_plugin_opener::OpenerExt;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::oneshot;

use crate::models::*;

/// Candidate loopback ports for the OAuth redirect, tried in order.
///
/// The Google OAuth client is a "Web application" client (shared with the web
/// build), so redirect URIs must match **exactly** — each of these must be
/// registered in the Google Cloud Console as an authorized redirect URI:
/// `http://localhost:17871/callback`, `http://localhost:17872/callback`,
/// `http://localhost:17873/callback`.
const REDIRECT_PORTS: [u16; 3] = [17871, 17872, 17873];

/// How long we wait for the user to finish signing in in their browser.
const FLOW_TIMEOUT: Duration = Duration::from_secs(300);

/// Per-connection budget for reading the request head + writing the response.
const CONNECTION_TIMEOUT: Duration = Duration::from_secs(10);

pub fn init<R: Runtime, C: DeserializeOwned>(
  app: &AppHandle<R>,
  _api: PluginApi<R, C>,
) -> crate::Result<GoogleOauth<R>> {
  Ok(GoogleOauth {
    app: app.clone(),
    cancel: Mutex::new(None),
  })
}

/// Access to the google-oauth APIs.
pub struct GoogleOauth<R: Runtime> {
  app: AppHandle<R>,
  /// Dropping this sender cancels the currently pending flow (a new sign-in
  /// attempt replaces — and thereby cancels — the previous one).
  cancel: Mutex<Option<oneshot::Sender<()>>>,
}

impl<R: Runtime> GoogleOauth<R> {
  pub async fn start_google_oauth(
    &self,
    payload: StartGoogleOauthRequest,
  ) -> crate::Result<GoogleOauthResult> {
    let app = self.app.clone();
    let url = validate_auth_url(&payload.auth_url)?;

    // Registering our cancel handle drops the previous flow's sender, which
    // resolves that flow with "cancelled" and frees its loopback port.
    let (cancel_tx, mut cancel_rx) = oneshot::channel::<()>();
    {
      let mut guard = self
        .cancel
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
      *guard = Some(cancel_tx);
    }

    let (listeners, port) = bind_loopback().await?;
    let state = random_state()?;
    let auth_url = rewrite_auth_url(url, port, &state);

    app
      .opener()
      .open_url(auth_url.as_str(), None::<&str>)
      .map_err(|e| crate::Error::Browser(e.to_string()))?;

    let result = tokio::select! {
      _ = &mut cancel_rx => GoogleOauthResult::cancelled(),
      served = tokio::time::timeout(FLOW_TIMEOUT, serve(listeners, state)) => match served {
        Ok(result) => result,
        Err(_) => GoogleOauthResult {
          id_token: None,
          error: Some("sign-in timed out".into()),
        },
      },
    };

    // The browser stole focus during the round-trip; bring the app back.
    if let Some(w) = app.get_webview_window("main") {
      let _ = w.show();
      let _ = w.unminimize();
      let _ = w.set_focus();
    }

    Ok(result)
  }
}

/// Loopback listeners. `localhost` may resolve to `::1` first on some
/// systems, so we bind the IPv6 loopback too when it is available.
struct Listeners {
  v4: TcpListener,
  v6: Option<TcpListener>,
}

impl Listeners {
  async fn accept(&self) -> std::io::Result<TcpStream> {
    match &self.v6 {
      Some(v6) => tokio::select! {
        r = self.v4.accept() => r.map(|(s, _)| s),
        r = v6.accept() => r.map(|(s, _)| s),
      },
      None => self.v4.accept().await.map(|(s, _)| s),
    }
  }
}

async fn bind_loopback() -> crate::Result<(Listeners, u16)> {
  for port in REDIRECT_PORTS {
    if let Ok(v4) = TcpListener::bind(("127.0.0.1", port)).await {
      let v6 = TcpListener::bind(("::1", port)).await.ok();
      return Ok((Listeners { v4, v6 }, port));
    }
  }
  Err(crate::Error::Port(format!(
    "ports {REDIRECT_PORTS:?} are all in use"
  )))
}

/// Accepts connections until one of them completes the flow.
async fn serve(listeners: Listeners, expected_state: String) -> GoogleOauthResult {
  loop {
    let Ok(mut stream) = listeners.accept().await else {
      return GoogleOauthResult {
        id_token: None,
        error: Some("local sign-in listener failed".into()),
      };
    };
    // Stray connections (favicon probes, speculative preconnects) must not
    // stall the flow; bound each connection and keep listening.
    if let Ok(Some(result)) = tokio::time::timeout(
      CONNECTION_TIMEOUT,
      handle_connection(&mut stream, &expected_state),
    )
    .await
    {
      return result;
    }
  }
}

/// Reads the HTTP request head and answers it. Returns the OAuth outcome once
/// a request completes the flow.
async fn handle_connection(
  stream: &mut TcpStream,
  expected_state: &str,
) -> Option<GoogleOauthResult> {
  let mut buf = Vec::with_capacity(2048);
  let mut chunk = [0u8; 1024];
  loop {
    let n = stream.read(&mut chunk).await.ok()?;
    if n == 0 {
      break;
    }
    buf.extend_from_slice(&chunk[..n]);
    if buf.windows(4).any(|w| w == b"\r\n\r\n") || buf.len() > 16 * 1024 {
      break;
    }
  }

  let head = String::from_utf8_lossy(&buf);
  let mut request_line = head.lines().next().unwrap_or_default().split_whitespace();
  let method = request_line.next().unwrap_or_default();
  let target = request_line.next().unwrap_or_default();

  let (response, outcome) = process_request(method, target, expected_state);
  let raw = format!(
    "HTTP/1.1 {}\r\nContent-Type: {}\r\nContent-Length: {}\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n{}",
    response.status,
    response.content_type,
    response.body.len(),
    response.body
  );
  let _ = stream.write_all(raw.as_bytes()).await;
  let _ = stream.shutdown().await;
  outcome
}

struct HttpResponse {
  status: &'static str,
  content_type: &'static str,
  body: String,
}

impl HttpResponse {
  fn html(body: String) -> Self {
    Self {
      status: "200 OK",
      content_type: "text/html; charset=utf-8",
      body,
    }
  }

  fn plain(status: &'static str, body: &str) -> Self {
    Self {
      status,
      content_type: "text/plain; charset=utf-8",
      body: body.into(),
    }
  }
}

/// Serves `/callback` (fragment-forwarding page) and `/finish` (token
/// delivery). Pure so the routing/state handling is unit-testable.
fn process_request(
  method: &str,
  target: &str,
  expected_state: &str,
) -> (HttpResponse, Option<GoogleOauthResult>) {
  if method != "GET" {
    return (
      HttpResponse::plain("405 Method Not Allowed", "method not allowed"),
      None,
    );
  }
  let Ok(url) = format!("http://localhost{target}").parse::<url::Url>() else {
    return (HttpResponse::plain("400 Bad Request", "bad request"), None);
  };

  match url.path() {
    "/callback" => {
      // Denials can arrive as plain query parameters; everything else lives
      // in the fragment, which only the page itself can see.
      if let Some(error) = query_value(&url, "error") {
        return (
          HttpResponse::html(done_page(false)),
          Some(GoogleOauthResult {
            id_token: None,
            error: Some(error),
          }),
        );
      }
      (HttpResponse::html(FORWARD_PAGE.into()), None)
    }
    "/finish" => {
      let result = result_from_query(&url);
      // The state echoed by Google must match before a credential is
      // accepted — nothing else on this machine can guess it.
      if result.id_token.is_some() && query_value(&url, "state").as_deref() != Some(expected_state)
      {
        return (HttpResponse::plain("400 Bad Request", "state mismatch"), None);
      }
      let ok = result.id_token.is_some();
      (HttpResponse::html(done_page(ok)), Some(result))
    }
    _ => (HttpResponse::plain("404 Not Found", "not found"), None),
  }
}

fn validate_auth_url(raw: &str) -> crate::Result<url::Url> {
  let url = raw
    .parse::<url::Url>()
    .map_err(|e| crate::Error::InvalidUrl(e.to_string()))?;
  if url.scheme() == "https" && url.host_str() == Some("accounts.google.com") {
    Ok(url)
  } else {
    Err(crate::Error::InvalidUrl(
      "expected https://accounts.google.com".into(),
    ))
  }
}

/// Points the auth URL at our loopback server and pins the response contract
/// (`response_type=id_token` + a fresh anti-forgery `state`).
fn rewrite_auth_url(mut url: url::Url, port: u16, state: &str) -> url::Url {
  let kept: Vec<(String, String)> = url
    .query_pairs()
    .filter(|(k, _)| {
      !matches!(
        k.as_ref(),
        "redirect_uri" | "response_type" | "response_mode" | "state"
      )
    })
    .map(|(k, v)| (k.into_owned(), v.into_owned()))
    .collect();
  {
    let mut q = url.query_pairs_mut();
    q.clear();
    for (k, v) in &kept {
      q.append_pair(k, v);
    }
    q.append_pair("redirect_uri", &format!("http://localhost:{port}/callback"));
    q.append_pair("response_type", "id_token");
    q.append_pair("state", state);
  }
  url
}

fn random_state() -> crate::Result<String> {
  let mut buf = [0u8; 32];
  getrandom::fill(&mut buf).map_err(|e| crate::Error::Internal(format!("randomness: {e}")))?;
  Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}

fn query_value(url: &url::Url, key: &str) -> Option<String> {
  url
    .query_pairs()
    .find(|(k, _)| k == key)
    .map(|(_, v)| v.into_owned())
}

fn result_from_query(url: &url::Url) -> GoogleOauthResult {
  let mut result = GoogleOauthResult::default();
  for (key, value) in url.query_pairs() {
    match key.as_ref() {
      "id_token" => result.id_token = Some(value.into_owned()),
      "error" => result.error = Some(value.into_owned()),
      _ => {}
    }
  }
  if result.id_token.is_none() && result.error.is_none() {
    result.error = Some("No credential received".into());
  }
  result
}

/// Served at `/callback`: moves the URL fragment (where Google puts the
/// id_token) into a query string the server can read.
const FORWARD_PAGE: &str = r#"<!doctype html>
<html><head><meta charset="utf-8"><title>Signing in…</title></head>
<body style="font-family:-apple-system,system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0">
<p>Completing sign-in…</p>
<script>
  var h = location.hash ? location.hash.slice(1) : "";
  location.replace("/finish" + (h ? "?" + h : "?error=no_credential"));
</script>
</body></html>"#;

fn done_page(ok: bool) -> String {
  let message = if ok {
    "You're signed in. You can close this tab and return to gratefulagents."
  } else {
    "Sign-in was not completed. You can close this tab and return to gratefulagents."
  };
  format!(
    r#"<!doctype html>
<html><head><meta charset="utf-8"><title>gratefulagents</title></head>
<body style="font-family:-apple-system,system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0">
<p>{message}</p>
<script>window.close()</script>
</body></html>"#
  )
}

#[cfg(test)]
mod tests {
  use super::*;

  #[test]
  fn validate_auth_url_accepts_google_https() {
    let url = validate_auth_url("https://accounts.google.com/o/oauth2/v2/auth?client_id=x")
      .expect("valid Google OAuth URL");
    assert_eq!(url.scheme(), "https");
    assert_eq!(url.host_str(), Some("accounts.google.com"));
  }

  #[test]
  fn validate_auth_url_rejects_non_google_or_non_https() {
    assert!(validate_auth_url("http://accounts.google.com/o/oauth2/v2/auth").is_err());
    assert!(validate_auth_url("https://evil.com/o/oauth2/v2/auth").is_err());
    assert!(validate_auth_url("file:///etc/passwd").is_err());
    assert!(validate_auth_url("not a url").is_err());
  }

  #[test]
  fn rewrite_auth_url_points_at_loopback_and_pins_contract() {
    let url = validate_auth_url(
      "https://accounts.google.com/o/oauth2/v2/auth?client_id=x&redirect_uri=http%3A%2F%2Flocalhost&response_type=id_token&scope=openid+email&nonce=n1&state=evil",
    )
    .unwrap();
    let out = rewrite_auth_url(url, 17871, "s3cret");
    assert_eq!(
      query_value(&out, "redirect_uri").as_deref(),
      Some("http://localhost:17871/callback")
    );
    assert_eq!(query_value(&out, "state").as_deref(), Some("s3cret"));
    assert_eq!(
      query_value(&out, "response_type").as_deref(),
      Some("id_token")
    );
    // Original params survive.
    assert_eq!(query_value(&out, "client_id").as_deref(), Some("x"));
    assert_eq!(query_value(&out, "nonce").as_deref(), Some("n1"));
    assert_eq!(query_value(&out, "scope").as_deref(), Some("openid email"));
  }

  #[test]
  fn callback_serves_forwarding_page() {
    let (resp, outcome) = process_request("GET", "/callback", "st");
    assert_eq!(resp.status, "200 OK");
    assert!(resp.body.contains("location.replace"));
    assert!(outcome.is_none());
  }

  #[test]
  fn callback_reports_query_errors_directly() {
    let (_, outcome) = process_request("GET", "/callback?error=access_denied", "st");
    let result = outcome.expect("flow finishes");
    assert_eq!(result.error.as_deref(), Some("access_denied"));
    assert!(result.id_token.is_none());
  }

  #[test]
  fn finish_accepts_token_with_matching_state() {
    let (resp, outcome) = process_request("GET", "/finish?id_token=tok&state=st", "st");
    assert_eq!(resp.status, "200 OK");
    let result = outcome.expect("flow finishes");
    assert_eq!(result.id_token.as_deref(), Some("tok"));
    assert!(result.error.is_none());
  }

  #[test]
  fn finish_rejects_token_with_wrong_or_missing_state() {
    let (resp, outcome) = process_request("GET", "/finish?id_token=tok&state=nope", "st");
    assert_eq!(resp.status, "400 Bad Request");
    assert!(outcome.is_none());

    let (resp, outcome) = process_request("GET", "/finish?id_token=tok", "st");
    assert_eq!(resp.status, "400 Bad Request");
    assert!(outcome.is_none());
  }

  #[test]
  fn finish_reports_errors_and_empty_results() {
    let (_, outcome) = process_request("GET", "/finish?error=access_denied", "st");
    assert_eq!(
      outcome.expect("finishes").error.as_deref(),
      Some("access_denied")
    );

    let (_, outcome) = process_request("GET", "/finish", "st");
    assert_eq!(
      outcome.expect("finishes").error.as_deref(),
      Some("No credential received")
    );
  }

  #[test]
  fn unknown_paths_and_methods_do_not_finish_the_flow() {
    let (resp, outcome) = process_request("GET", "/favicon.ico", "st");
    assert_eq!(resp.status, "404 Not Found");
    assert!(outcome.is_none());

    let (resp, outcome) = process_request("POST", "/finish?id_token=tok&state=st", "st");
    assert_eq!(resp.status, "405 Method Not Allowed");
    assert!(outcome.is_none());
  }
}
