use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StartGoogleOauthRequest {
  pub auth_url: String,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GoogleOauthResult {
  pub id_token: Option<String>,
  pub error: Option<String>,
}

impl GoogleOauthResult {
  pub fn cancelled() -> Self {
    Self {
      id_token: None,
      error: Some("cancelled".into()),
    }
  }
}
