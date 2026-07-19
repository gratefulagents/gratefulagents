use tauri::{
  plugin::{Builder, TauriPlugin},
  Manager, Runtime,
};

pub use models::*;

#[cfg(desktop)]
mod desktop;
#[cfg(mobile)]
mod mobile;

mod commands;
mod error;
mod models;

pub use error::{Error, Result};

#[cfg(desktop)]
use desktop::GoogleOauth;
#[cfg(mobile)]
use mobile::GoogleOauth;

/// Extensions to [`tauri::App`], [`tauri::AppHandle`] and [`tauri::Window`] to access the google-oauth APIs.
pub trait GoogleOauthExt<R: Runtime> {
  fn google_oauth(&self) -> &GoogleOauth<R>;
}

impl<R: Runtime, T: Manager<R>> crate::GoogleOauthExt<R> for T {
  fn google_oauth(&self) -> &GoogleOauth<R> {
    self.state::<GoogleOauth<R>>().inner()
  }
}

/// Initializes the plugin.
pub fn init<R: Runtime>() -> TauriPlugin<R> {
  Builder::new("google-oauth")
    .invoke_handler(tauri::generate_handler![commands::start_google_oauth])
    .setup(|app, api| {
      #[cfg(mobile)]
      let google_oauth = mobile::init(app, api)?;
      #[cfg(desktop)]
      let google_oauth = desktop::init(app, api)?;
      app.manage(google_oauth);
      Ok(())
    })
    .build()
}
