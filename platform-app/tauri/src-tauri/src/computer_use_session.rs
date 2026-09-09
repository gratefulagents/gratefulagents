use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant};

use tauri::{AppHandle, Emitter, Manager, Runtime};

const LEASE: Duration = Duration::from_secs(10);

#[derive(Clone, Debug, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionScope {
    pub backend: String,
    pub user: String,
    pub namespace: String,
    pub run: String,
    pub application: String,
}

#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SessionPhase {
    Stopped,
    Active,
    Paused,
}

#[derive(Clone, Debug, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionStatus {
    pub phase: SessionPhase,
    pub session_id: Option<String>,
    pub scope: Option<SessionScope>,
    pub reason: String,
}

struct Session {
    id: String,
    scope: SessionScope,
    deadline: Instant,
    paused: bool,
}

#[derive(Default)]
struct SessionPolicy {
    session: Option<Session>,
    reason: String,
}

impl SessionPolicy {
    fn expire(&mut self, now: Instant) {
        if self
            .session
            .as_ref()
            .is_some_and(|session| now >= session.deadline)
        {
            self.stop("Desktop connection expired");
        }
    }

    fn stop(&mut self, reason: &str) {
        self.session = None;
        self.reason = reason.to_owned();
    }

    fn start(&mut self, id: String, scope: SessionScope, now: Instant) -> Result<(), String> {
        self.expire(now);
        if self.session.is_some() {
            return Err("Stop the current session before starting another".into());
        }
        let backend = url::Url::parse(&scope.backend).map_err(|_| "Invalid backend origin")?;
        if backend.scheme() != "https"
            || backend.host_str().is_none()
            || !backend.username().is_empty()
            || backend.password().is_some()
            || backend.query().is_some()
            || backend.fragment().is_some()
            || backend.path() != "/"
        {
            return Err("Computer use requires an HTTPS backend origin".into());
        }
        if [
            &scope.user,
            &scope.namespace,
            &scope.run,
            &scope.application,
        ]
        .iter()
        .any(|value| value.trim().is_empty() || value.len() > 256)
        {
            return Err("A user, run, namespace, and approved application are required".into());
        }
        self.session = Some(Session {
            id,
            scope,
            deadline: now + LEASE,
            paused: false,
        });
        self.reason.clear();
        Ok(())
    }

    fn bound(
        &mut self,
        id: &str,
        scope: &SessionScope,
        now: Instant,
    ) -> Result<&mut Session, String> {
        self.expire(now);
        let session = self.session.as_mut().ok_or("No active desktop session")?;
        if session.id != id || &session.scope != scope {
            return Err("Desktop session binding does not match".into());
        }
        Ok(session)
    }

    fn heartbeat(&mut self, id: &str, scope: &SessionScope, now: Instant) -> Result<(), String> {
        self.bound(id, scope, now)?.deadline = now + LEASE;
        Ok(())
    }

    fn pause(&mut self, reason: &str) {
        if let Some(session) = &mut self.session {
            session.paused = true;
            self.reason = reason.to_owned();
        }
    }

    fn resume(&mut self, id: &str, scope: &SessionScope, now: Instant) -> Result<(), String> {
        self.bound(id, scope, now)?.paused = false;
        self.reason.clear();
        Ok(())
    }

    fn status(&mut self, now: Instant) -> SessionStatus {
        self.expire(now);
        SessionStatus {
            phase: match &self.session {
                None => SessionPhase::Stopped,
                Some(session) if session.paused => SessionPhase::Paused,
                Some(_) => SessionPhase::Active,
            },
            session_id: self.session.as_ref().map(|session| session.id.clone()),
            scope: self.session.as_ref().map(|session| session.scope.clone()),
            reason: self.reason.clone(),
        }
    }
}

#[derive(Default)]
pub struct ComputerUseSession {
    policy: Mutex<SessionPolicy>,
    pub shortcut_ready: AtomicBool,
}

pub fn stop<R: Runtime>(app: &AppHandle<R>, reason: &str) {
    let state = app.state::<ComputerUseSession>();
    let mut policy = state
        .policy
        .lock()
        .unwrap_or_else(|error| error.into_inner());
    policy.stop(reason);
    let status = policy.status(Instant::now());
    drop(policy);
    let _ = app.emit("computer-use://status", status);
}

fn require_permissions(state: &ComputerUseSession) -> Result<(), String> {
    let permissions = crate::computer_use::computer_use_permissions();
    if !permissions.supported {
        return Err("Computer use requires the macOS desktop app".into());
    }
    if !state.shortcut_ready.load(Ordering::SeqCst) {
        return Err("The native emergency stop shortcut is unavailable".into());
    }
    if !permissions.screen_recording || !permissions.accessibility {
        return Err("Screen Recording and Accessibility permissions are required".into());
    }
    Ok(())
}

#[tauri::command]
pub fn computer_use_session_start(
    state: tauri::State<'_, ComputerUseSession>,
    scope: SessionScope,
    consent_to_screen_sharing: bool,
) -> Result<SessionStatus, String> {
    require_permissions(&state)?;
    if !consent_to_screen_sharing {
        return Err("Explicit consent to share screen content is required".into());
    }
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|error| error.to_string())?;
    let id: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    let now = Instant::now();
    policy.start(id, scope, now)?;
    Ok(policy.status(now))
}

#[tauri::command]
pub fn computer_use_session_status(
    state: tauri::State<'_, ComputerUseSession>,
) -> Result<SessionStatus, String> {
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    if policy.session.is_some() && require_permissions(&state).is_err() {
        policy.pause("Required OS permission or emergency stop is unavailable");
    }
    Ok(policy.status(Instant::now()))
}

#[tauri::command]
pub fn computer_use_session_heartbeat(
    state: tauri::State<'_, ComputerUseSession>,
    session_id: String,
    scope: SessionScope,
) -> Result<(), String> {
    state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?
        .heartbeat(&session_id, &scope, Instant::now())
}

#[tauri::command]
pub fn computer_use_session_pause(app: AppHandle) {
    let state = app.state::<ComputerUseSession>();
    let mut policy = state
        .policy
        .lock()
        .unwrap_or_else(|error| error.into_inner());
    policy.pause("Paused by supervisor");
    let status = policy.status(Instant::now());
    drop(policy);
    let _ = app.emit("computer-use://status", status);
}

#[tauri::command]
pub fn computer_use_session_resume(
    state: tauri::State<'_, ComputerUseSession>,
    session_id: String,
    scope: SessionScope,
) -> Result<SessionStatus, String> {
    require_permissions(&state)?;
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    let now = Instant::now();
    policy.resume(&session_id, &scope, now)?;
    Ok(policy.status(now))
}

#[tauri::command]
pub fn computer_use_session_stop(app: AppHandle) {
    stop(&app, "Stopped by supervisor");
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scope() -> SessionScope {
        SessionScope {
            backend: "https://operator.example".into(),
            user: "user-1".into(),
            namespace: "default".into(),
            run: "run-1".into(),
            application: "com.apple.TextEdit".into(),
        }
    }

    #[test]
    fn starts_stopped() {
        assert_eq!(
            SessionPolicy::default().status(Instant::now()).phase,
            SessionPhase::Stopped
        );
    }

    #[test]
    fn start_is_bound_and_cannot_replace_an_existing_session() {
        let now = Instant::now();
        let mut policy = SessionPolicy::default();
        policy.start("session-1".into(), scope(), now).unwrap();
        assert_eq!(policy.status(now).phase, SessionPhase::Active);
        assert_eq!(policy.status(now).scope, Some(scope()));
        assert!(policy.start("session-2".into(), scope(), now).is_err());
    }

    #[test]
    fn expiry_cannot_be_revived_by_a_late_heartbeat_or_resume() {
        let now = Instant::now();
        let mut policy = SessionPolicy::default();
        policy.start("s".into(), scope(), now).unwrap();
        assert!(policy.heartbeat("s", &scope(), now + LEASE).is_err());
        assert!(policy.resume("s", &scope(), now + LEASE).is_err());
        assert_eq!(policy.status(now + LEASE).phase, SessionPhase::Stopped);
    }

    #[test]
    fn heartbeat_extends_lease_but_never_resumes_paused_session() {
        let now = Instant::now();
        let mut policy = SessionPolicy::default();
        policy.start("s".into(), scope(), now).unwrap();
        policy.pause("focus changed");
        policy
            .heartbeat("s", &scope(), now + Duration::from_secs(5))
            .unwrap();
        assert_eq!(policy.status(now + LEASE).phase, SessionPhase::Paused);
        assert_eq!(policy.status(now + LEASE).reason, "focus changed");
    }

    #[test]
    fn stop_invalidates_old_bindings_even_after_new_start() {
        let now = Instant::now();
        let mut policy = SessionPolicy::default();
        policy.start("old".into(), scope(), now).unwrap();
        policy.stop("emergency stop");
        assert!(policy.resume("old", &scope(), now).is_err());
        policy.start("new".into(), scope(), now).unwrap();
        assert!(policy.heartbeat("old", &scope(), now).is_err());
    }

    #[test]
    fn every_scope_field_is_bound() {
        let now = Instant::now();
        let mut policy = SessionPolicy::default();
        policy.start("s".into(), scope(), now).unwrap();
        for field in 0..5 {
            let mut changed = scope();
            match field {
                0 => changed.backend = "https://other.example".into(),
                1 => changed.user = "other".into(),
                2 => changed.namespace = "other".into(),
                3 => changed.run = "other".into(),
                _ => changed.application = "other".into(),
            }
            assert!(policy.heartbeat("s", &changed, now).is_err());
            assert!(policy.resume("s", &changed, now).is_err());
        }
    }

    #[test]
    fn rejects_invalid_transport_origins_and_empty_scope() {
        let now = Instant::now();
        for backend in [
            "http://example.com",
            "file:///tmp/socket",
            "https://user:pass@example.com",
            "https://example.com/path",
            "https://example.com?token=x",
        ] {
            let mut invalid = scope();
            invalid.backend = backend.into();
            assert!(SessionPolicy::default()
                .start("s".into(), invalid, now)
                .is_err());
        }
        let mut invalid = scope();
        invalid.run.clear();
        assert!(SessionPolicy::default()
            .start("s".into(), invalid, now)
            .is_err());
    }
}
