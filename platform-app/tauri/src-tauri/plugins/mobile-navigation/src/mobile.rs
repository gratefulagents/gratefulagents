use serde::de::DeserializeOwned;
use tauri::{plugin::PluginApi, Runtime};

#[cfg(target_os = "ios")]
tauri::ios_plugin_binding!(init_plugin_mobile_navigation);

pub fn init<R: Runtime, C: DeserializeOwned>(
  api: PluginApi<R, C>,
) -> Result<(), tauri::plugin::mobile::PluginInvokeError> {
  #[cfg(target_os = "android")]
  api.register_android_plugin(
    "dev.gratefulagents.app.mobilenavigation",
    "MobileNavigationPlugin",
  )?;
  #[cfg(target_os = "ios")]
  api.register_ios_plugin(init_plugin_mobile_navigation)?;
  Ok(())
}
