use tauri::{
  plugin::{Builder, TauriPlugin},
  Runtime,
};

#[cfg(mobile)]
mod mobile;

/// Enables native navigation behavior for the app's WebView on mobile.
///
/// iOS gets WKWebView's interactive back/forward swipe gestures. Android's
/// system Back action first traverses WebView history, then falls through to
/// the activity when the app is already at the root page.
pub fn init<R: Runtime>() -> TauriPlugin<R> {
  Builder::new("mobile-navigation")
    .setup(|_app, _api| {
      #[cfg(mobile)]
      mobile::init(_api)?;
      Ok(())
    })
    .build()
}
