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
 * must be registered on the Google OAuth client). Android uses Credential
 * Manager's native Sign in with Google flow and requires an Android OAuth
 * client for the app package/signing certificate. iOS presents a native
 * authentication session.
 */
export async function startGoogleOauth(authUrl: string): Promise<GoogleOauthResult> {
  return await invoke<GoogleOauthResult>('plugin:google-oauth|start_google_oauth', {
    payload: { authUrl },
  })
}
