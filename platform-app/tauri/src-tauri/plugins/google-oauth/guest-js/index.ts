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
 * must be registered on the Google OAuth client). Android uses a Chrome
 * Custom Tab with `http://127.0.0.1:17871-17873/callback`; those three exact
 * URIs must also be registered. iOS presents a native authentication session.
 */
export async function startGoogleOauth(authUrl: string): Promise<GoogleOauthResult> {
  return await invoke<GoogleOauthResult>('plugin:google-oauth|start_google_oauth', {
    payload: { authUrl },
  })
}
