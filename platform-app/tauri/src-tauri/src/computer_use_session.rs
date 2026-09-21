use super::computer_use_capture::DisplayCapture;
use super::computer_use_input::{Action, Frame, QueuedRequest, FRAME_TTL};
use std::collections::{HashSet, VecDeque};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use tauri::{AppHandle, Emitter, Manager, Runtime};

const QUEUE_TTL: Duration = Duration::from_secs(120);
const EXECUTION_TTL: Duration = Duration::from_secs(5);
// Typing delivers each character as its own checked key pair, so the permit
// grows with the proposed text; the result stays well below QUEUE_TTL.
const TYPE_UNIT_TTL: Duration = Duration::from_millis(40);
const MAX_REQUEST_IDS: usize = 1024;
const MAX_FRAMES: usize = 4;

const LEASE: Duration = Duration::from_secs(10);

fn execution_ttl(action: &Action) -> Duration {
    match action {
        Action::Type { text } => {
            EXECUTION_TTL + TYPE_UNIT_TTL * text.encode_utf16().count().min(1000) as u32
        }
        _ => EXECUTION_TTL,
    }
}

#[derive(Clone, Debug, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SessionMode {
    SelectedDisplay,
    #[serde(other)]
    Legacy,
}

#[derive(Clone, Debug, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", try_from = "serde_json::Value")]
pub struct SessionScope {
    pub mode: SessionMode,
    pub backend: String,
    pub user: String,
    pub namespace: String,
    pub run: String,
    pub display_id: u32,
}

impl TryFrom<serde_json::Value> for SessionScope {
    type Error = String;
    fn try_from(value: serde_json::Value) -> Result<Self, Self::Error> {
        #[derive(serde::Deserialize)]
        #[serde(rename_all = "camelCase", deny_unknown_fields)]
        struct Wire {
            mode: SessionMode,
            backend: String,
            user: String,
            namespace: String,
            run: String,
            display_id: u32,
        }
        let migration = "Legacy or invalid window scope: update the desktop and reconnect with selected-display capture and desktop-wide input consent";
        let wire: Wire = serde_json::from_value(value).map_err(|_| migration.to_owned())?;
        if wire.mode != SessionMode::SelectedDisplay || wire.display_id == 0 {
            return Err(migration.into());
        }
        Ok(Self {
            mode: wire.mode,
            backend: wire.backend,
            user: wire.user,
            namespace: wire.namespace,
            run: wire.run,
            display_id: wire.display_id,
        })
    }
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
    pub revision: u64,
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
    pending: Option<Pending>,
    seen: HashSet<String>,
    frames: VecDeque<Frame>,
    running: Option<(String, Arc<AtomicBool>)>,
}

struct Pending {
    request: QueuedRequest,
    deadline: Instant,
    armed: Option<(String, Instant)>,
}

impl Session {
    fn invalidate(&mut self) {
        self.pending = None;
        self.frames.clear();
        if let Some((_, canceled)) = self.running.take() {
            canceled.store(true, Ordering::SeqCst);
        }
    }

    fn fresh_frame(&self, id: &str, now: Instant) -> Result<&Frame, String> {
        self.frames
            .back()
            .filter(|f| f.id == id && now.duration_since(f.created) < FRAME_TTL)
            .ok_or_else(|| "Frame is stale, consumed, or unavailable; capture again".into())
    }
}

struct Execution {
    request: QueuedRequest,
    frame: Option<Frame>,
    deadline: Instant,
    canceled: Arc<AtomicBool>,
    revision: u64,
}

pub fn random_id() -> Result<String, String> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|error| error.to_string())?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

#[derive(Default)]
struct SessionPolicy {
    revision: u64,
    session: Option<Session>,
    reason: String,
}

impl SessionPolicy {
    fn require_revision(&self, expected: u64) -> Result<(), String> {
        if self.revision != expected {
            return Err("Desktop authorization changed; request fresh consent".into());
        }
        Ok(())
    }

    fn expire(&mut self, now: Instant) {
        if let Some(session) = &mut self.session {
            if session.pending.as_ref().is_some_and(|p| {
                now >= p.deadline
                    || p.armed
                        .as_ref()
                        .is_some_and(|(_, deadline)| now >= *deadline)
            }) {
                session.pending = None;
            }
            session
                .frames
                .retain(|frame| now.duration_since(frame.created) < FRAME_TTL);
        }
        if self
            .session
            .as_ref()
            .is_some_and(|session| now >= session.deadline)
        {
            self.stop("Desktop connection expired");
        }
    }

    fn stop(&mut self, reason: &str) {
        self.revision = self.revision.wrapping_add(1);
        if let Some(session) = &mut self.session {
            session.invalidate();
        }
        self.session = None;
        super::computer_use_picker::clear();
        self.reason = reason.to_owned();
    }

    fn start(&mut self, id: String, scope: SessionScope, now: Instant) -> Result<(), String> {
        self.expire(now);
        if self.session.is_some() {
            return Err("Stop the current session before starting another".into());
        }
        if scope.backend.len() > 2048 {
            return Err("Invalid backend origin".into());
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
        if [&scope.user, &scope.namespace, &scope.run]
            .iter()
            .any(|value| value.trim().is_empty() || value.len() > 256)
        {
            return Err("A user, run, namespace, and selected display are required".into());
        }
        if scope.mode != SessionMode::SelectedDisplay || scope.display_id == 0 {
            return Err("Legacy window scope is unsupported; reconnect with selected-display and desktop-input consent".into());
        }
        self.session = Some(Session {
            id,
            scope,
            deadline: now + LEASE,
            paused: false,
            pending: None,
            seen: HashSet::new(),
            frames: VecDeque::new(),
            running: None,
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

    fn active(
        &mut self,
        id: &str,
        scope: &SessionScope,
        now: Instant,
    ) -> Result<&mut Session, String> {
        let session = self.bound(id, scope, now)?;
        if session.paused {
            return Err("Desktop session is paused".into());
        }
        Ok(session)
    }

    fn queue(
        &mut self,
        id: &str,
        scope: &SessionScope,
        request: QueuedRequest,
        now: Instant,
    ) -> Result<(), String> {
        request.validate()?;
        let session = self.active(id, scope, now)?;
        if !matches!(request.action, Action::Observe { .. }) && request.frame_id.is_none() {
            return Err("Observe the selected display before input".into());
        }
        if let Some(pending) = &session.pending {
            return if pending.request == request {
                Ok(())
            } else {
                Err("A different desktop request is already pending".into())
            };
        }
        if session.running.is_some() {
            return Err("Desktop execution is in progress".into());
        }
        if session.seen.contains(&request.request_id) {
            return Err("Desktop request ID was already used".into());
        }
        if session.seen.len() >= MAX_REQUEST_IDS {
            self.stop("Desktop request replay limit reached; start a new session");
            return Err("Desktop request replay limit reached".into());
        }
        let session = self.active(id, scope, now)?;
        if let Some(frame_id) = &request.frame_id {
            session
                .fresh_frame(frame_id, now)?
                .points(&request.action)?;
        }
        session.seen.insert(request.request_id.clone());
        session.pending = Some(Pending {
            request,
            deadline: now + QUEUE_TTL,
            armed: None,
        });
        Ok(())
    }

    fn arm(
        &mut self,
        id: &str,
        scope: &SessionScope,
        request_id: &str,
        permit: String,
        now: Instant,
    ) -> Result<String, String> {
        let session = self.active(id, scope, now)?;
        let pending = session
            .pending
            .as_ref()
            .ok_or("No pending desktop request")?;
        if pending.request.request_id != request_id {
            return Err("Pending desktop request does not match".into());
        }
        if pending.armed.is_some() {
            return Err("Desktop request is already armed".into());
        }
        if let Some(frame_id) = &pending.request.frame_id {
            session.fresh_frame(frame_id, now)?;
        }
        let pending = session.pending.as_mut().unwrap();
        let ttl = execution_ttl(&pending.request.action);
        pending.armed = Some((permit.clone(), (now + ttl).min(pending.deadline)));
        Ok(permit)
    }

    fn consume(
        &mut self,
        id: &str,
        scope: &SessionScope,
        request_id: &str,
        permit: &str,
        now: Instant,
    ) -> Result<Execution, String> {
        let revision = self.revision;
        let session = self.active(id, scope, now)?;
        if session
            .pending
            .as_ref()
            .is_none_or(|p| p.request.request_id != request_id)
        {
            return Err("No matching pending desktop request".into());
        }
        let pending = session.pending.take().unwrap();
        let (expected, deadline) = pending
            .armed
            .ok_or("Desktop request has not been approved/armed")?;
        if permit != expected || now >= deadline {
            return Err("Invalid or expired single-use desktop permit".into());
        }
        let frame = pending
            .request
            .frame_id
            .as_ref()
            .map(|id| session.fresh_frame(id, now).cloned())
            .transpose()?;
        session.frames.clear();
        let canceled = Arc::new(AtomicBool::new(false));
        session.running = Some((request_id.to_owned(), canceled.clone()));
        Ok(Execution {
            request: pending.request,
            frame,
            deadline,
            canceled,
            revision,
        })
    }

    fn cancel(
        &mut self,
        id: &str,
        scope: &SessionScope,
        request_id: &str,
        now: Instant,
    ) -> Result<(), String> {
        let session = self.bound(id, scope, now)?;
        let pending = session
            .pending
            .as_ref()
            .is_some_and(|p| p.request.request_id == request_id);
        let running = session
            .running
            .as_ref()
            .is_some_and(|(id, _)| id == request_id);
        if !pending && !running {
            return if session.seen.contains(request_id) {
                Ok(())
            } else {
                Err("No matching desktop request".into())
            };
        }
        session.invalidate();
        self.revision = self.revision.wrapping_add(1);
        Ok(())
    }

    fn record_frame(
        &mut self,
        id: &str,
        scope: &SessionScope,
        revision: u64,
        frame: Frame,
        now: Instant,
    ) -> Result<(), String> {
        self.require_revision(revision)?;
        let session = self.active(id, scope, now)?;
        if now.duration_since(frame.created) >= FRAME_TTL {
            return Err("Capture expired before delivery".into());
        }
        if session.frames.len() == MAX_FRAMES {
            session.frames.pop_front();
        }
        session.frames.push_back(frame);
        Ok(())
    }

    fn heartbeat(&mut self, id: &str, scope: &SessionScope, now: Instant) -> Result<(), String> {
        self.bound(id, scope, now)?.deadline = now + LEASE;
        Ok(())
    }

    fn pause(&mut self, reason: &str) {
        if let Some(session) = &mut self.session {
            self.revision = self.revision.wrapping_add(1);
            session.paused = true;
            session.invalidate();
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
            revision: self.revision,
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
    capture_busy: AtomicBool,
    picker_busy: AtomicBool,
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
        return Err("Computer use requires the macOS desktop app on macOS 15.2 or later".into());
    }
    if !state.shortcut_ready.load(Ordering::SeqCst) {
        return Err("The native emergency stop shortcut is unavailable".into());
    }
    if !permissions.accessibility {
        return Err("Accessibility permission is required".into());
    }
    super::computer_use::require_screen_permission()
}

#[tauri::command]
pub fn computer_use_pick_window() -> Result<(), String> {
    Err("Window control was removed; update desktop/backend/run and reconnect with selected-display capture and desktop-wide input consent".into())
}

#[tauri::command]
pub fn computer_use_capture_window() -> Result<(), String> {
    Err("Window control was removed; update desktop/backend/run and reconnect with selected-display capture and desktop-wide input consent".into())
}

#[tauri::command]
pub async fn computer_use_pick_display(
    app: AppHandle,
    expected_revision: u64,
) -> Result<Option<super::computer_use_capture::DisplayTarget>, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<ComputerUseSession>();
        require_permissions(&state)?;
        if state.picker_busy.swap(true, Ordering::SeqCst) {
            return Err("The macOS display picker is already open".into());
        }
        let _busy = Busy(&state.picker_busy);
        let mut policy = state
            .policy
            .lock()
            .map_err(|_| "Desktop session state unavailable")?;
        policy.expire(Instant::now());
        policy.require_revision(expected_revision)?;
        if policy.session.is_some() {
            return Err("Stop the current session before selecting another display".into());
        }
        super::computer_use_picker::clear();
        let picking = super::computer_use_picker::begin()?;
        drop(policy);
        let picked = picking.wait()?;
        let policy = state
            .policy
            .lock()
            .map_err(|_| "Desktop session state unavailable")?;
        policy.require_revision(expected_revision)?;
        if policy.session.is_some() {
            return Err("Desktop authorization changed".into());
        }
        Ok(picked.install())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub fn computer_use_session_start(
    state: tauri::State<'_, ComputerUseSession>,
    scope: SessionScope,
    consent_to_screen_sharing: bool,
    consent_to_desktop_input: bool,
    selection_id: String,
    expected_revision: u64,
) -> Result<SessionStatus, String> {
    require_permissions(&state)?;
    if !consent_to_screen_sharing || !consent_to_desktop_input {
        return Err(
            "Explicit selected-display capture and desktop-wide input consent are required".into(),
        );
    }

    let id = random_id()?;
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    let now = Instant::now();
    policy.expire(now);
    policy.require_revision(expected_revision)?;
    if scope.mode != SessionMode::SelectedDisplay {
        return Err(
            "Legacy window mode is unsupported; reconnect with selected-display desktop consent"
                .into(),
        );
    }
    super::computer_use_picker::bind(&selection_id, &scope)?;
    policy.start(id, scope, now)?;
    Ok(policy.status(now))
}

fn inspect_session(state: &ComputerUseSession) -> Result<SessionStatus, String> {
    let observed = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?
        .status(Instant::now());
    if observed.phase != SessionPhase::Active {
        return Ok(observed);
    }
    let permissions_available = require_permissions(state).is_ok();
    let display_available = permissions_available
        && observed
            .scope
            .as_ref()
            .is_some_and(|scope| super::computer_use_capture::validate_target(scope).is_ok());
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    if policy.revision == observed.revision {
        if !permissions_available {
            policy.pause("Required OS permission or emergency stop is unavailable");
        } else if !display_available {
            policy.pause("Selected display identity or capture is no longer available");
        }
    }
    Ok(policy.status(Instant::now()))
}

#[cfg(target_os = "macos")]
pub fn watch<R: Runtime>(app: AppHandle<R>) {
    std::thread::spawn(move || {
        let mut last_revision = None;
        loop {
            let state = app.state::<ComputerUseSession>();
            match inspect_session(&state) {
                Ok(status) => {
                    if last_revision != Some(status.revision) {
                        last_revision = Some(status.revision);
                        let _ = app.emit("computer-use://status", status);
                    }
                }
                Err(_) => stop(&app, "Desktop supervision is unavailable"),
            }
            std::thread::sleep(Duration::from_millis(500));
        }
    });
}

#[tauri::command]
pub fn computer_use_session_status(
    state: tauri::State<'_, ComputerUseSession>,
) -> Result<SessionStatus, String> {
    inspect_session(&state)
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
    super::computer_use_capture::validate_target(&scope)?;
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

#[tauri::command]
pub async fn computer_use_capture_display(
    app: AppHandle,
    session_id: String,
    scope: SessionScope,
) -> Result<super::computer_use_capture::DisplayCapture, String> {
    let state = app.state::<ComputerUseSession>();
    require_permissions(&state)?;
    let revision = {
        let mut policy = state
            .policy
            .lock()
            .map_err(|_| "Desktop session state unavailable")?;
        policy.active(&session_id, &scope, Instant::now())?;
        policy.revision
    };
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<ComputerUseSession>();
        let _busy = state.acquire()?;
        let created = Instant::now();
        check_execution(&state, &session_id, &scope, revision, None)?;
        let target = current_target(&state, &session_id, &scope)?;
        let result = super::computer_use_capture::capture(&target);
        check_execution(&state, &session_id, &scope, revision, None)?;
        let mut policy = state
            .policy
            .lock()
            .map_err(|_| "Desktop session state unavailable")?;
        match result {
            Ok(capture) => {
                policy.record_frame(
                    &session_id,
                    &scope,
                    revision,
                    Frame::from_capture(&capture, created),
                    Instant::now(),
                )?;
                Ok(capture)
            }
            Err(error) => {
                policy.pause("Capture failed; verify the selected display before resuming");
                Err(error)
            }
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

struct Busy<'a>(&'a AtomicBool);
impl Drop for Busy<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::SeqCst);
    }
}
impl ComputerUseSession {
    fn acquire(&self) -> Result<Busy<'_>, String> {
        if self.capture_busy.swap(true, Ordering::SeqCst) {
            return Err("Desktop capture or execution is already in progress".into());
        }
        Ok(Busy(&self.capture_busy))
    }
}

fn current_target(
    state: &ComputerUseSession,
    id: &str,
    scope: &SessionScope,
) -> Result<SessionScope, String> {
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    policy.active(id, scope, Instant::now())?;
    Ok(scope.clone())
}

fn check_execution(
    state: &ComputerUseSession,
    id: &str,
    scope: &SessionScope,
    revision: u64,
    execution: Option<&Execution>,
) -> Result<(), String> {
    require_permissions(state)?;
    super::computer_use_capture::validate_target(scope)?;
    let now = Instant::now();
    if execution.is_some_and(|e| e.canceled.load(Ordering::SeqCst) || now >= e.deadline) {
        return Err("Desktop execution canceled or expired".into());
    }
    let mut policy = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?;
    policy.active(id, scope, now)?;
    policy.require_revision(revision)?;
    Ok(())
}

#[derive(serde::Serialize)]
pub struct ArmedRequest {
    permit: String,
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RequestOutcome {
    request_id: String,
    status: &'static str,
    message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    capture: Option<DisplayCapture>,
}

#[tauri::command]
pub fn computer_use_queue_request(
    state: tauri::State<'_, ComputerUseSession>,
    session_id: String,
    scope: SessionScope,
    request: QueuedRequest,
) -> Result<(), String> {
    require_permissions(&state)?;
    request.validate()?;
    super::computer_use_capture::validate_target(&scope)?;
    state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?
        .queue(&session_id, &scope, request, Instant::now())?;
    Ok(())
}

#[tauri::command]
pub fn computer_use_arm_request(
    state: tauri::State<'_, ComputerUseSession>,
    session_id: String,
    scope: SessionScope,
    request_id: String,
) -> Result<ArmedRequest, String> {
    require_permissions(&state)?;
    let permit = random_id()?;
    let permit = state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?
        .arm(&session_id, &scope, &request_id, permit, Instant::now())?;
    Ok(ArmedRequest { permit })
}

#[tauri::command]
pub fn computer_use_cancel_request(
    state: tauri::State<'_, ComputerUseSession>,
    session_id: String,
    scope: SessionScope,
    request_id: String,
) -> Result<(), String> {
    state
        .policy
        .lock()
        .map_err(|_| "Desktop session state unavailable")?
        .cancel(&session_id, &scope, &request_id, Instant::now())
}

#[tauri::command]
pub async fn computer_use_approve_request(
    app: AppHandle,
    session_id: String,
    scope: SessionScope,
    request_id: String,
    permit: String,
) -> Result<RequestOutcome, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<ComputerUseSession>();
        // Serialize with local preview/capture before consuming the single-use
        // permit: a busy executor must not destroy the request or pause the session.
        let busy = state.acquire()?;
        let execution = {
            let mut policy = state
                .policy
                .lock()
                .map_err(|_| "Desktop session state unavailable")?;
            policy.consume(&session_id, &scope, &request_id, &permit, Instant::now())?
        };
        let run = || -> Result<Option<DisplayCapture>, String> {
            let check = || {
                check_execution(
                    &state,
                    &session_id,
                    &scope,
                    execution.revision,
                    Some(&execution),
                )
            };
            check()?;
            let selected = current_target(&state, &session_id, &scope)?;
            if matches!(execution.request.action, Action::Observe { .. }) {
                let created = Instant::now();
                let capture = super::computer_use_capture::capture(&selected)?;
                check()?;
                state
                    .policy
                    .lock()
                    .map_err(|_| "Desktop session state unavailable")?
                    .record_frame(
                        &session_id,
                        &scope,
                        execution.revision,
                        Frame::from_capture(&capture, created),
                        Instant::now(),
                    )?;
                Ok(Some(capture))
            } else {
                super::computer_use_input::execute(
                    &selected,
                    &execution.request.action,
                    execution.frame.as_ref(),
                    &check,
                )?;
                check()?;
                Ok(None)
            }
        };
        let mut result = run();
        drop(busy);
        let mut policy = state
            .policy
            .lock()
            .map_err(|_| "Desktop session state unavailable")?;
        if result.is_ok() {
            let now = Instant::now();
            if execution.canceled.load(Ordering::SeqCst)
                || now >= execution.deadline
                || policy.require_revision(execution.revision).is_err()
                || policy.active(&session_id, &scope, now).is_err()
            {
                result = Err("Desktop execution invalidated before delivery".into());
            }
        }
        if let Ok(session) = policy.bound(&session_id, &scope, Instant::now()) {
            if session
                .running
                .as_ref()
                .is_some_and(|(_, token)| Arc::ptr_eq(token, &execution.canceled))
            {
                session.running = None;
                if result.is_err() {
                    policy.pause("Desktop action failed; verify the target before resuming");
                }
            }
        }
        let (status, message, capture) = match result {
            Ok(capture) => (
                "completed",
                "Approved desktop request completed".into(),
                capture,
            ),
            Err(message) => ("failed", message, None),
        };
        Ok(RequestOutcome {
            request_id,
            status,
            message,
            capture,
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scope() -> SessionScope {
        SessionScope {
            mode: SessionMode::SelectedDisplay,
            backend: "https://operator.example".into(),
            user: "user-1".into(),
            namespace: "default".into(),
            run: "run-1".into(),
            display_id: 42,
        }
    }

    fn request(id: &str) -> QueuedRequest {
        QueuedRequest {
            request_id: id.into(),
            frame_id: None,
            action: Action::Observe { question: None },
        }
    }

    fn policy(now: Instant) -> SessionPolicy {
        let mut policy = SessionPolicy::default();
        policy.start("s".into(), scope(), now).unwrap();
        policy
    }

    fn frame(id: &str, now: Instant) -> Frame {
        Frame {
            id: id.into(),
            geometry: super::super::computer_use_capture::DisplayBounds {
                x: -100,
                y: 50,
                width: 100,
                height: 50,
            },
            pixel_width: 200,
            pixel_height: 100,
            displays: vec![],
            created: now,
        }
    }

    #[test]
    fn queue_is_exactly_idempotent_and_permits_are_single_use() {
        let now = Instant::now();
        let mut p = policy(now);
        p.queue("s", &scope(), request("r"), now).unwrap();
        p.queue("s", &scope(), request("r"), now).unwrap();
        assert!(p.queue("s", &scope(), request("other"), now).is_err());
        let mut changed = request("r");
        changed.action = Action::Observe {
            question: Some("changed".into()),
        };
        assert!(p.queue("s", &scope(), changed, now).is_err());
        p.arm("s", &scope(), "r", "secret".into(), now).unwrap();
        assert!(p.arm("s", &scope(), "r", "other".into(), now).is_err());
        p.consume("s", &scope(), "r", "secret", now).unwrap();
        assert!(p.consume("s", &scope(), "r", "secret", now).is_err());
        p.session.as_mut().unwrap().running = None;
        assert!(p.queue("s", &scope(), request("r"), now).is_err());
    }

    #[test]
    fn unarmed_or_wrong_permit_consumes_request() {
        let now = Instant::now();
        for armed in [false, true] {
            let mut p = policy(now);
            p.queue("s", &scope(), request("r"), now).unwrap();
            if armed {
                p.arm("s", &scope(), "r", "secret".into(), now).unwrap();
            }
            assert!(p.consume("s", &scope(), "r", "wrong", now).is_err());
            assert!(p.queue("s", &scope(), request("r"), now).is_err());
        }
    }

    #[test]
    fn queue_and_arm_deadlines_do_not_extend_on_retry_or_heartbeat() {
        let now = Instant::now();
        let mut p = policy(now);
        p.queue("s", &scope(), request("r"), now).unwrap();
        p.arm("s", &scope(), "r", "secret".into(), now).unwrap();
        p.heartbeat("s", &scope(), now + Duration::from_secs(4))
            .unwrap();
        assert!(p
            .consume("s", &scope(), "r", "secret", now + EXECUTION_TTL)
            .is_err());
        let mut p = policy(now);
        p.queue("s", &scope(), request("r"), now).unwrap();
        for second in (5..120).step_by(5) {
            let time = now + Duration::from_secs(second);
            p.heartbeat("s", &scope(), time).unwrap();
            p.queue("s", &scope(), request("r"), time).unwrap();
        }
        assert!(p
            .arm("s", &scope(), "r", "secret".into(), now + QUEUE_TTL)
            .is_err());
        assert!(p
            .queue("s", &scope(), request("r"), now + QUEUE_TTL)
            .is_err());
    }

    #[test]
    fn typing_permit_scales_with_text_but_stays_bounded() {
        let now = Instant::now();
        let typed = |units: usize| QueuedRequest {
            request_id: "t".into(),
            frame_id: Some("f".into()),
            action: Action::Type {
                text: "a".repeat(units),
            },
        };
        assert_eq!(
            execution_ttl(&Action::Observe { question: None }),
            EXECUTION_TTL
        );
        assert_eq!(
            execution_ttl(&Action::Key {
                key: "Enter".into()
            }),
            EXECUTION_TTL
        );
        let short = execution_ttl(&typed(1).action);
        let long = execution_ttl(&typed(1000).action);
        assert!(short > EXECUTION_TTL && short < EXECUTION_TTL + Duration::from_secs(1));
        assert!(long > short && long < QUEUE_TTL / 2);
        assert_eq!(execution_ttl(&typed(5000).action), long);

        let mut p = policy(now);
        p.record_frame("s", &scope(), p.revision, frame("f", now), now)
            .unwrap();
        p.queue("s", &scope(), typed(200), now).unwrap();
        p.arm("s", &scope(), "t", "secret".into(), now).unwrap();
        // A 200-unit permit outlives the fixed window but still expires.
        assert!(p
            .consume(
                "s",
                &scope(),
                "t",
                "secret",
                now + EXECUTION_TTL + Duration::from_secs(1)
            )
            .is_ok());
        let mut p = policy(now);
        p.record_frame("s", &scope(), p.revision, frame("f", now), now)
            .unwrap();
        p.queue("s", &scope(), typed(200), now).unwrap();
        p.arm("s", &scope(), "t", "secret".into(), now).unwrap();
        assert!(p
            .consume(
                "s",
                &scope(),
                "t",
                "secret",
                now + execution_ttl(&typed(200).action)
            )
            .is_err());
    }

    #[test]
    fn cancellation_pause_stop_and_expiry_invalidate_work_and_frames() {
        let now = Instant::now();
        for transition in 0..4 {
            let mut p = policy(now);
            p.queue("s", &scope(), request("r"), now).unwrap();
            p.arm("s", &scope(), "r", "secret".into(), now).unwrap();
            let execution = p.consume("s", &scope(), "r", "secret", now).unwrap();
            p.record_frame("s", &scope(), p.revision, frame("f", now), now)
                .unwrap();
            match transition {
                0 => p.cancel("s", &scope(), "r", now).unwrap(),
                1 => p.pause("paused"),
                2 => p.stop("stopped"),
                _ => p.expire(now + LEASE),
            }
            assert!(execution.canceled.load(Ordering::SeqCst));
            assert!(p.require_revision(execution.revision).is_err());
            assert!(p
                .session
                .as_ref()
                .is_none_or(|s| s.frames.is_empty() && s.pending.is_none()));
            assert!(p
                .record_frame("s", &scope(), execution.revision, frame("late", now), now)
                .is_err());
        }
    }

    #[test]
    fn cancel_and_pause_do_not_allow_pending_replay() {
        let now = Instant::now();
        for pause in [false, true] {
            let mut p = policy(now);
            p.queue("s", &scope(), request("r"), now).unwrap();
            p.arm("s", &scope(), "r", "secret".into(), now).unwrap();
            if pause {
                p.pause("paused");
                p.resume("s", &scope(), now).unwrap();
            } else {
                p.cancel("s", &scope(), "r", now).unwrap();
                p.cancel("s", &scope(), "r", now).unwrap();
            }
            assert!(p.consume("s", &scope(), "r", "secret", now).is_err());
            assert!(p.queue("s", &scope(), request("r"), now).is_err());
        }
    }

    #[test]
    fn replay_memory_cap_stops_instead_of_evicting_ids() {
        let now = Instant::now();
        let mut p = policy(now);
        for index in 0..MAX_REQUEST_IDS {
            let id = index.to_string();
            p.queue("s", &scope(), request(&id), now).unwrap();
            p.cancel("s", &scope(), &id, now).unwrap();
        }
        assert!(p.queue("s", &scope(), request("next"), now).is_err());
        assert_eq!(p.status(now).phase, SessionPhase::Stopped);
    }

    #[test]
    fn frame_registry_is_bounded_fresh_and_single_use() {
        let now = Instant::now();
        let mut p = policy(now);
        for i in 0..MAX_FRAMES + 1 {
            p.record_frame("s", &scope(), p.revision, frame(&i.to_string(), now), now)
                .unwrap();
        }
        assert_eq!(p.session.as_ref().unwrap().frames.len(), MAX_FRAMES);
        assert!(p.session.as_ref().unwrap().fresh_frame("0", now).is_err());
        assert!(p.session.as_ref().unwrap().fresh_frame("1", now).is_err());
        assert!(p
            .session
            .as_ref()
            .unwrap()
            .fresh_frame("1", now + FRAME_TTL)
            .is_err());
        let click = QueuedRequest {
            request_id: "click".into(),
            frame_id: Some(MAX_FRAMES.to_string()),
            action: Action::Click {
                x: 100.0,
                y: 50.0,
                button: None,
                count: None,
            },
        };
        p.queue("s", &scope(), click, now).unwrap();
        p.arm("s", &scope(), "click", "permit".into(), now).unwrap();
        assert!(p
            .consume("s", &scope(), "click", "permit", now)
            .unwrap()
            .frame
            .is_some());
        assert!(p.session.as_ref().unwrap().frames.is_empty());
    }

    #[test]
    fn scope_binding_applies_to_every_request_operation() {
        let now = Instant::now();
        let mut p = policy(now);
        p.queue("s", &scope(), request("r"), now).unwrap();
        let mut wrong = scope();
        wrong.run = "other".into();
        assert!(p.queue("s", &wrong, request("r"), now).is_err());
        assert!(p.arm("s", &wrong, "r", "p".into(), now).is_err());
        assert!(p.cancel("s", &wrong, "r", now).is_err());
        assert!(p.consume("s", &wrong, "r", "p", now).is_err());
        p.arm("s", &scope(), "r", "p".into(), now).unwrap();
        p.consume("s", &scope(), "r", "p", now).unwrap();
    }

    #[test]
    fn native_work_serialization_releases_on_drop() {
        let state = ComputerUseSession::default();
        let busy = state.acquire().unwrap();
        assert!(state.acquire().is_err());
        drop(busy);
        assert!(state.acquire().is_ok());
    }

    #[test]
    fn starts_stopped() {
        assert_eq!(
            SessionPolicy::default().status(Instant::now()).phase,
            SessionPhase::Stopped
        );
    }

    #[test]
    fn inspection_expires_session_without_a_frontend_heartbeat() {
        let state = ComputerUseSession::default();
        state
            .policy
            .lock()
            .unwrap()
            .start("s".into(), scope(), Instant::now() - LEASE)
            .unwrap();
        let status = inspect_session(&state).unwrap();
        assert_eq!(status.phase, SessionPhase::Stopped);
        assert_eq!(status.reason, "Desktop connection expired");
        assert!(status.session_id.is_none());
    }

    #[test]
    fn inspection_does_not_reauthorize_a_paused_session() {
        let state = ComputerUseSession::default();
        {
            let mut policy = state.policy.lock().unwrap();
            policy.start("s".into(), scope(), Instant::now()).unwrap();
            policy.pause("supervisor paused");
        }
        let status = inspect_session(&state).unwrap();
        assert_eq!(status.phase, SessionPhase::Paused);
        assert_eq!(status.reason, "supervisor paused");
    }

    #[cfg(not(target_os = "macos"))]
    #[test]
    fn inspection_pauses_when_native_permissions_are_unavailable() {
        let state = ComputerUseSession::default();
        state
            .policy
            .lock()
            .unwrap()
            .start("s".into(), scope(), Instant::now())
            .unwrap();
        let status = inspect_session(&state).unwrap();
        assert_eq!(status.phase, SessionPhase::Paused);
        assert_eq!(
            status.reason,
            "Required OS permission or emergency stop is unavailable"
        );
    }

    #[test]
    fn stop_invalidates_queued_start_revision_even_without_an_active_session() {
        let mut policy = SessionPolicy::default();
        let consent_revision = policy.revision;
        policy.stop("emergency stop");
        assert!(policy.require_revision(consent_revision).is_err());
    }

    #[test]
    fn pause_then_resume_does_not_reauthorize_an_in_flight_capture() {
        let now = Instant::now();
        let mut policy = SessionPolicy::default();
        policy.start("s".into(), scope(), now).unwrap();
        let capture_revision = policy.revision;
        policy.pause("supervisor paused");
        policy.resume("s", &scope(), now).unwrap();
        assert_eq!(policy.status(now).phase, SessionPhase::Active);
        assert!(policy.require_revision(capture_revision).is_err());
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
                _ => changed.display_id += 1,
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

#[cfg(test)]
mod desktop_migration_tests {
    use super::*;
    #[test]
    fn old_scopes_cannot_reconnect_or_inherit_desktop_consent() {
        assert!(computer_use_pick_window()
            .unwrap_err()
            .contains("reconnect"));
        assert!(computer_use_capture_window()
            .unwrap_err()
            .contains("reconnect"));
        for raw in [
            r#"{"backend":"https://example.com","user":"u","namespace":"n","run":"r","windowId":42,"processId":1,"application":"App"}"#,
            r#"{"mode":"agent_choice","backend":"https://example.com","user":"u","namespace":"n","run":"r","displayId":42}"#,
        ] {
            assert!(serde_json::from_str::<SessionScope>(raw)
                .unwrap_err()
                .to_string()
                .contains("reconnect"));
        }
        let scope: SessionScope = serde_json::from_str(r#"{"mode":"selected_display","backend":"https://example.com","user":"u","namespace":"n","run":"r","displayId":42}"#).unwrap();
        assert_eq!(scope.display_id, 42);
    }
    #[test]
    fn native_implementation_uses_display_capture_and_global_events() {
        let capture = include_str!("computer_use_picker.m");
        assert!(capture.contains("initWithDisplay:selected excludingWindows:@[]"));
        assert!(!capture.contains("SCWindow"));
        let input = include_str!("computer_use_input.rs");
        assert!(input.contains("CGEventPost(0,"));
        assert!(!input.contains("CGEventPostToPid"));
        assert!(!input.contains("ga_ax_window"));
    }
}
