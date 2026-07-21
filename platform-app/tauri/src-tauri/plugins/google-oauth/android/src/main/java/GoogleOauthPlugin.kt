package com.gratefulagents.operator.googleoauth

import android.annotation.SuppressLint
import android.app.Activity
import android.app.Dialog
import android.net.Uri
import android.view.Gravity
import android.view.ViewGroup
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import app.tauri.annotation.Command
import app.tauri.annotation.InvokeArg
import app.tauri.annotation.TauriPlugin
import app.tauri.plugin.Invoke
import app.tauri.plugin.JSObject
import app.tauri.plugin.Plugin
import java.net.URI

@InvokeArg
class StartGoogleOauthArgs {
  var authUrl: String = ""
}

/**
 * Presents the web-client Google OAuth flow in an app-owned WebView, matching
 * the iOS implementation. The localhost redirect is intercepted before any
 * network request and its ID token is returned to the frontend for nonce and
 * server-side verification.
 */
@TauriPlugin
class GoogleOauthPlugin(private val activity: Activity) : Plugin(activity) {
  private data class ActiveFlow(
    val invoke: Invoke,
    val dialog: Dialog,
    val webView: WebView
  )

  private val stateLock = Any()
  private var pendingInvoke: Invoke? = null
  private var activeFlow: ActiveFlow? = null

  @Command
  fun startGoogleOauth(invoke: Invoke) {
    val args = invoke.parseArgs(StartGoogleOauthArgs::class.java)
    val authUri = validateAuthUrl(args.authUrl)
    if (authUri == null) {
      invoke.resolve(errorResult("InvalidUrl"))
      return
    }

    synchronized(stateLock) {
      if (pendingInvoke != null) {
        invoke.resolve(errorResult("sign-in already in progress"))
        return
      }
      pendingInvoke = invoke
    }

    activity.runOnUiThread {
      try {
        presentSignIn(invoke, authUri)
      } catch (error: Exception) {
        fail(invoke, errorResult("Unable to open Google Sign-In: ${error.message ?: "unknown error"}"))
      }
    }
  }

  @SuppressLint("SetJavaScriptEnabled")
  private fun presentSignIn(invoke: Invoke, authUri: Uri) {
    val dialog = Dialog(activity, android.R.style.Theme_DeviceDefault_Light_NoActionBar)
    val root = LinearLayout(activity).apply {
      orientation = LinearLayout.VERTICAL
      layoutParams = ViewGroup.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.MATCH_PARENT
      )
    }
    val cancel = Button(activity).apply {
      text = "Cancel"
      gravity = Gravity.START or Gravity.CENTER_VERTICAL
      setOnClickListener { dialog.cancel() }
    }
    val webView = WebView(activity).apply {
      layoutParams = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        0,
        1f
      )
      settings.javaScriptEnabled = true
      settings.domStorageEnabled = true
      // As on iOS, use a regular mobile-browser user agent. Google otherwise
      // rejects the embedded WebView user agent before account selection.
      settings.userAgentString =
        "Mozilla/5.0 (Linux; Android 14; Mobile) AppleWebKit/537.36 " +
        "(KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
      webChromeClient = WebChromeClient()
    }
    root.addView(cancel)
    root.addView(webView)
    dialog.setContentView(root)
    dialog.setCanceledOnTouchOutside(false)

    val flow = ActiveFlow(invoke, dialog, webView)
    synchronized(stateLock) {
      if (pendingInvoke !== invoke) {
        webView.destroy()
        return
      }
      activeFlow = flow
    }

    webView.webViewClient = object : WebViewClient() {
      override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
        return interceptRedirect(flow, request.url)
      }

      @Suppress("DEPRECATION")
      override fun shouldOverrideUrlLoading(view: WebView, url: String): Boolean {
        return interceptRedirect(flow, Uri.parse(url))
      }
    }
    dialog.setOnCancelListener { finish(flow, errorResult("cancelled")) }
    dialog.setOnDismissListener { finish(flow, errorResult("cancelled")) }
    dialog.show()
    dialog.window?.setLayout(
      ViewGroup.LayoutParams.MATCH_PARENT,
      ViewGroup.LayoutParams.MATCH_PARENT
    )
    webView.loadUrl(authUri.toString())
  }

  private fun interceptRedirect(flow: ActiveFlow, uri: Uri): Boolean {
    if (!isRedirect(uri)) return false
    finish(flow, parseRedirect(uri))
    return true
  }

  private fun isRedirect(uri: Uri): Boolean {
    if (uri.scheme != "http") return false
    return uri.host == "localhost" || uri.host == "127.0.0.1"
  }

  private fun parseRedirect(uri: Uri): JSObject {
    val fragment = uri.encodedFragment
    val fragmentParams = fragment?.let { Uri.parse("https://localhost/?$it") }
    val idToken = fragmentParams?.getQueryParameter("id_token")
    val error = fragmentParams?.getQueryParameter("error")
      ?: uri.getQueryParameter("error")

    return JSObject().apply {
      when {
        !idToken.isNullOrBlank() -> put("idToken", idToken)
        !error.isNullOrBlank() -> put("error", error)
        else -> put("error", "No credential received")
      }
    }
  }

  private fun validateAuthUrl(raw: String): Uri? {
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
    return Uri.parse(raw)
  }

  private fun fail(invoke: Invoke, result: JSObject) {
    val flow = synchronized(stateLock) {
      activeFlow?.takeIf { it.invoke === invoke }
    }
    if (flow != null) {
      finish(flow, result)
    } else {
      finishSetup(invoke, result)
    }
  }

  private fun finishSetup(invoke: Invoke, result: JSObject) {
    synchronized(stateLock) {
      if (pendingInvoke !== invoke || activeFlow != null) return
      pendingInvoke = null
    }
    invoke.resolve(result)
  }

  private fun finish(flow: ActiveFlow, result: JSObject) {
    val invoke: Invoke
    synchronized(stateLock) {
      if (activeFlow !== flow || pendingInvoke !== flow.invoke) return
      activeFlow = null
      pendingInvoke = null
      invoke = flow.invoke
    }

    flow.dialog.setOnCancelListener(null)
    flow.dialog.setOnDismissListener(null)
    flow.webView.stopLoading()
    flow.dialog.dismiss()
    flow.webView.destroy()
    invoke.resolve(result)
  }

  private fun errorResult(message: String): JSObject = JSObject().apply {
    put("error", message)
  }
}
