package com.gratefulagents.operator.googleoauth

import android.app.Activity
import android.net.Uri
import androidx.credentials.CredentialManager
import androidx.credentials.CustomCredential
import androidx.credentials.GetCredentialRequest
import androidx.credentials.exceptions.GetCredentialCancellationException
import androidx.credentials.exceptions.GetCredentialException
import app.tauri.annotation.Command
import app.tauri.annotation.InvokeArg
import app.tauri.annotation.TauriPlugin
import app.tauri.plugin.Invoke
import app.tauri.plugin.JSObject
import app.tauri.plugin.Plugin
import com.google.android.libraries.identity.googleid.GetSignInWithGoogleOption
import com.google.android.libraries.identity.googleid.GoogleIdTokenCredential
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import java.net.URI

@InvokeArg
class StartGoogleOauthArgs {
  var authUrl: String = ""
}

private data class GoogleSignInRequest(
  val clientId: String,
  val nonce: String
)

/**
 * Runs the native Sign in with Google button flow through Android Credential
 * Manager. Native Android apps must not use a desktop-style loopback redirect:
 * Google associates sign-in with this app's package and signing certificate and
 * returns an ID token whose audience is the configured web client ID.
 */
@TauriPlugin
class GoogleOauthPlugin(private val activity: Activity) : Plugin(activity) {
  private val credentialManager = CredentialManager.create(activity)
  private val coroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
  private val stateLock = Any()
  private var signInPending = false

  @Command
  fun startGoogleOauth(invoke: Invoke) {
    val args = invoke.parseArgs(StartGoogleOauthArgs::class.java)
    val signInRequest = parseAuthUrl(args.authUrl)
    if (signInRequest == null) {
      invoke.resolve(errorResult("Invalid Google Sign-In request"))
      return
    }

    synchronized(stateLock) {
      if (signInPending) {
        invoke.resolve(errorResult("sign-in already in progress"))
        return
      }
      signInPending = true
    }

    coroutineScope.launch {
      val result = try {
        requestCredential(signInRequest)
      } catch (_: GetCredentialCancellationException) {
        errorResult("cancelled")
      } catch (error: GetCredentialException) {
        errorResult(error.message ?: "Google Sign-In failed")
      } catch (error: Exception) {
        errorResult(error.message ?: "Google Sign-In failed")
      }

      synchronized(stateLock) {
        signInPending = false
      }
      invoke.resolve(result)
    }
  }

  private suspend fun requestCredential(signInRequest: GoogleSignInRequest): JSObject {
    val googleOption = GetSignInWithGoogleOption.Builder(signInRequest.clientId)
      .setNonce(signInRequest.nonce)
      .build()
    val request = GetCredentialRequest.Builder()
      .addCredentialOption(googleOption)
      .build()
    val response = credentialManager.getCredential(activity, request)
    val credential = response.credential

    if (credential !is CustomCredential ||
      credential.type != GoogleIdTokenCredential.TYPE_GOOGLE_ID_TOKEN_CREDENTIAL) {
      return errorResult("Google Sign-In returned an unsupported credential")
    }

    val idToken = GoogleIdTokenCredential.createFrom(credential.data).idToken
    return JSObject().apply { put("idToken", idToken) }
  }

  /**
   * The frontend already builds the cross-platform Google authorization URL.
   * On Android, only its validated web client ID and nonce are needed by the
   * native Credential Manager request; no browser navigation is performed.
   */
  private fun parseAuthUrl(raw: String): GoogleSignInRequest? {
    if (raw.any { it == '\\' || it.code < 0x20 || it.code == 0x7f }) return null
    val parsed = try {
      URI(raw)
    } catch (_: Exception) {
      return null
    }
    if (parsed.scheme != "https" || parsed.rawAuthority != "accounts.google.com" ||
      parsed.userInfo != null || parsed.port != -1 || parsed.host != "accounts.google.com") {
      return null
    }

    val uri = Uri.parse(raw)
    val clientId = uri.getQueryParameter("client_id")?.takeIf { it.isNotBlank() } ?: return null
    val nonce = uri.getQueryParameter("nonce")?.takeIf { it.isNotBlank() } ?: return null
    return GoogleSignInRequest(clientId, nonce)
  }

  private fun errorResult(message: String): JSObject = JSObject().apply {
    put("error", message)
  }
}
