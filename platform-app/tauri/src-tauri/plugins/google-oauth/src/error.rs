use serde::{ser::Serializer, Serialize};

pub type Result<T> = std::result::Result<T, Error>;

#[derive(Debug, thiserror::Error)]
pub enum Error {
  #[error(transparent)]
  Io(#[from] std::io::Error),
  #[error("invalid auth url: {0}")]
  InvalidUrl(String),
  #[cfg(desktop)]
  #[error("failed to open the system browser: {0}")]
  Browser(String),
  #[cfg(desktop)]
  #[error("could not bind a loopback port for the sign-in redirect: {0}")]
  Port(String),
  #[cfg(desktop)]
  #[error("{0}")]
  Internal(String),
  #[cfg(mobile)]
  #[error(transparent)]
  PluginInvoke(#[from] tauri::plugin::mobile::PluginInvokeError),
}

impl Serialize for Error {
  fn serialize<S>(&self, serializer: S) -> std::result::Result<S::Ok, S::Error>
  where
    S: Serializer,
  {
    serializer.serialize_str(self.to_string().as_ref())
  }
}
