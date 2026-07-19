use tauri::{command, AppHandle, Runtime};

use crate::models::*;
use crate::GoogleOauthExt;
use crate::Result;

#[command]
pub(crate) async fn start_google_oauth<R: Runtime>(
  app: AppHandle<R>,
  payload: StartGoogleOauthRequest,
) -> Result<GoogleOauthResult> {
  app.google_oauth().start_google_oauth(payload).await
}
