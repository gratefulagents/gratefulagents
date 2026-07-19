use serde::de::DeserializeOwned;
use tauri::{
  plugin::{PluginApi, PluginHandle},
  AppHandle, Runtime,
};

use crate::models::*;

#[cfg(target_os = "ios")]
tauri::ios_plugin_binding!(init_plugin_google_oauth);

// initializes the Kotlin or Swift plugin classes
pub fn init<R: Runtime, C: DeserializeOwned>(
  _app: &AppHandle<R>,
  api: PluginApi<R, C>,
) -> crate::Result<GoogleOauth<R>> {
  #[cfg(target_os = "android")]
  let handle = api.register_android_plugin("com.gratefulagents.operator.googleoauth", "GoogleOauthPlugin")?;
  #[cfg(target_os = "ios")]
  let handle = api.register_ios_plugin(init_plugin_google_oauth)?;
  Ok(GoogleOauth(handle))
}

/// Access to the google-oauth APIs.
pub struct GoogleOauth<R: Runtime>(PluginHandle<R>);

impl<R: Runtime> GoogleOauth<R> {
  pub async fn start_google_oauth(
    &self,
    payload: StartGoogleOauthRequest,
  ) -> crate::Result<GoogleOauthResult> {
    // The native side opens a secure system-browser authentication session
    // and resolves after its loopback callback completes, so run it off the
    // async runtime worker thread for the duration of the flow.
    let handle = self.0.clone();
    tauri::async_runtime::spawn_blocking(move || {
      handle
        .run_mobile_plugin::<GoogleOauthResult>("startGoogleOauth", payload)
        .map_err(crate::Error::from)
    })
    .await
    .unwrap_or_else(|_| Ok(GoogleOauthResult::cancelled()))
  }
}
