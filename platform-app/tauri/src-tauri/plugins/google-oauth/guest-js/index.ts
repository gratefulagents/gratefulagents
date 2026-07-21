import { invoke } from '@tauri-apps/api/core'

export interface GoogleOauthResult {
  idToken: string | null
  error: string | null
}

/**
 * Starts Google sign-in and resolves with the returned id_token.
 *
 * Desktop (macOS/Windows/Linux): opens the system default browser — required
 * for passkey/WebAuthn support — and receives the OAuth redirect on a
 * loopback HTTP server (`http://localhost:17871-17873/callback`; those URIs
 * must be registered on the Google OAuth client). Android and iOS present the
 * web-client authorization flow in an app-owned authentication view and
 * intercept its `http://localhost` redirect; neither requires a separate
 * mobile OAuth client ID.
 */
export async function startGoogleOauth(authUrl: string): Promise<GoogleOauthResult> {
  return await invoke<GoogleOauthResult>('plugin:google-oauth|start_google_oauth', {
    payload: { authUrl },
  })
}
