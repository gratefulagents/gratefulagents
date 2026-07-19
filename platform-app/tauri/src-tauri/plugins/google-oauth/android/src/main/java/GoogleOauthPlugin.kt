package com.gratefulagents.operator.googleoauth

import android.app.Activity
import android.net.Uri
import android.os.SystemClock
import androidx.activity.result.ActivityResult
import androidx.browser.customtabs.CustomTabsIntent
import app.tauri.annotation.ActivityCallback
import app.tauri.annotation.Command
import app.tauri.annotation.InvokeArg
import app.tauri.annotation.TauriPlugin
import app.tauri.plugin.Invoke
import app.tauri.plugin.JSObject
import app.tauri.plugin.Plugin
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.net.URI
import java.nio.charset.StandardCharsets
import java.security.SecureRandom
import java.util.concurrent.Executors

@InvokeArg
class StartGoogleOauthArgs {
  var authUrl: String = ""
}

/**
 * Runs Google OAuth in the user's browser, as required by RFC 8252 and Google's
 * secure-browser policy. A short-lived loopback server receives the redirect.
 *
 * Google returns implicit-flow ID tokens in the URL fragment, which is never
 * sent in an HTTP request. The callback page therefore forwards the fragment
 * to /finish, where this plugin validates the OAuth state before resolving the
 * Tauri invocation.
 */
@TauriPlugin
class GoogleOauthPlugin(private val activity: Activity) : Plugin(activity) {
  companion object {
    private val REDIRECT_PORTS = intArrayOf(17871, 17872, 17873)
    private const val FLOW_TIMEOUT_MS = 300_000L
    private const val CONNECTION_TIMEOUT_MS = 10_000
    private const val MAX_REQUEST_HEAD_CHARS = 16 * 1024
  }

  private data class ActiveFlow(
    val invoke: Invoke,
    val state: String,
    val server: ServerSocket
  )

  private val stateLock = Any()
  private val executor = Executors.newCachedThreadPool()
  private var pendingInvoke: Invoke? = null
  private var activeFlow: ActiveFlow? = null
  private var flowSerial = 0

  @Command
  fun startGoogleOauth(invoke: Invoke) {
    val args = invoke.parseArgs(StartGoogleOauthArgs::class.java)
    val authUri = validateAuthUrl(args.authUrl)
    if (authUri == null) {
      invoke.resolve(errorResult("InvalidUrl"))
      return
    }

    val flowId: Int
    synchronized(stateLock) {
      if (pendingInvoke != null) {
        invoke.resolve(errorResult("sign-in already in progress"))
        return
      }
      flowSerial += 1
      flowId = flowSerial
      pendingInvoke = invoke
    }

    executor.execute {
      val server = bindLoopback()
      if (server == null) {
        finishSetup(flowId, invoke, errorResult("No available sign-in callback port"))
        return@execute
      }

      val state = randomState()
      val flow = ActiveFlow(invoke, state, server)
      synchronized(stateLock) {
        if (pendingInvoke !== invoke || flowSerial != flowId) {
          server.close()
          return@execute
        }
        activeFlow = flow
      }

      val browserUri = rewriteAuthUrl(authUri, server.localPort, state)
      activity.runOnUiThread {
        if (!isActive(flow)) return@runOnUiThread
        try {
          val customTab = CustomTabsIntent.Builder().build()
          customTab.intent.data = browserUri
          startActivityForResult(invoke, customTab.intent, "browserResult")
        } catch (error: Exception) {
          finish(flow, errorResult("Unable to open a secure browser: ${error.message ?: "unknown error"}"))
        }
      }

      serve(flow)
    }
  }

  @ActivityCallback
  private fun browserResult(invoke: Invoke, @Suppress("UNUSED_PARAMETER") result: ActivityResult) {
    val flow = synchronized(stateLock) {
      activeFlow?.takeIf { it.invoke === invoke }
    }
    if (flow != null) {
      finish(flow, errorResult("cancelled"))
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
    // Rebuild the origin instead of passing Android/Chromium an authority that
    // could be interpreted differently. Preserve only path, query and fragment.
    val path = parsed.rawPath.ifEmpty { "/" }
    return Uri.parse("https://accounts.google.com$path" +
      (parsed.rawQuery?.let { "?$it" } ?: "") +
      (parsed.rawFragment?.let { "#$it" } ?: ""))
  }

  private fun bindLoopback(): ServerSocket? {
    // Use one numeric loopback address in both the listener and redirect URI.
    // This avoids independent localhost IPv4/IPv6 resolution and prevents the
    // callback from being exposed on Wi-Fi or another non-loopback interface.
    val loopback = InetAddress.getByName("127.0.0.1")
    for (port in REDIRECT_PORTS) {
      try {
        return ServerSocket(port, 16, loopback).apply { soTimeout = 1_000 }
      } catch (_: Exception) {
        // Try the next Google-registered callback port.
      }
    }
    return null
  }

  private fun rewriteAuthUrl(uri: Uri, port: Int, state: String): Uri {
    val builder = uri.buildUpon().clearQuery()
    for (name in uri.queryParameterNames) {
      if (name == "redirect_uri" || name == "response_type" ||
        name == "response_mode" || name == "state") {
        continue
      }
      for (value in uri.getQueryParameters(name)) {
        builder.appendQueryParameter(name, value)
      }
    }
    return builder
      .appendQueryParameter("redirect_uri", "http://127.0.0.1:$port/callback")
      .appendQueryParameter("response_type", "id_token")
      .appendQueryParameter("state", state)
      .build()
  }

  private fun randomState(): String {
    val bytes = ByteArray(32)
    SecureRandom().nextBytes(bytes)
    return bytes.joinToString("") { "%02x".format(it) }
  }

  private fun serve(flow: ActiveFlow) {
    val deadline = SystemClock.elapsedRealtime() + FLOW_TIMEOUT_MS
    try {
      while (isActive(flow) && SystemClock.elapsedRealtime() < deadline) {
        val socket = try {
          flow.server.accept()
        } catch (_: SocketTimeoutException) {
          continue
        }
        try {
          socket.use {
            val result = handleConnection(it, flow.state)
            if (result != null) {
              finish(flow, result)
              return
            }
          }
        } catch (_: Exception) {
          // A browser probe or unrelated loopback client must not end OAuth.
        }
      }
      if (isActive(flow)) {
        finish(flow, errorResult("sign-in timed out"))
      }
    } catch (_: Exception) {
      if (isActive(flow)) {
        finish(flow, errorResult("local sign-in listener failed"))
      }
    }
  }

  private fun handleConnection(socket: Socket, expectedState: String): JSObject? {
    val deadline = SystemClock.elapsedRealtime() + CONNECTION_TIMEOUT_MS
    val input = socket.getInputStream()
    val requestBytes = ByteArray(MAX_REQUEST_HEAD_CHARS)
    var size = 0
    var headerComplete = false
    while (size < requestBytes.size && !headerComplete) {
      val remaining = deadline - SystemClock.elapsedRealtime()
      if (remaining <= 0) throw SocketTimeoutException("request timed out")
      socket.soTimeout = remaining.coerceAtMost(Int.MAX_VALUE.toLong()).toInt()
      val previousSize = size
      val count = input.read(requestBytes, size, requestBytes.size - size)
      if (count < 0) break
      size += count
      val scanStart = (previousSize - 3).coerceAtLeast(0)
      for (index in scanStart..(size - 4).coerceAtLeast(scanStart - 1)) {
        if (requestBytes[index] == '\r'.code.toByte() &&
          requestBytes[index + 1] == '\n'.code.toByte() &&
          requestBytes[index + 2] == '\r'.code.toByte() &&
          requestBytes[index + 3] == '\n'.code.toByte()) {
          headerComplete = true
          break
        }
      }
    }
    if (!headerComplete) {
      writeResponse(socket, "413 Payload Too Large", "text/plain; charset=utf-8", "request too large")
      return null
    }

    val requestHead = String(requestBytes, 0, size, StandardCharsets.US_ASCII)
    val requestLine = requestHead.substringBefore("\r\n")
    val parts = requestLine.split(' ')
    if (parts.size < 2 || parts[0] != "GET") {
      writeResponse(socket, "405 Method Not Allowed", "text/plain; charset=utf-8", "method not allowed")
      return null
    }

    val requestUri = try {
      Uri.parse("http://127.0.0.1${parts[1]}")
    } catch (_: Exception) {
      writeResponse(socket, "400 Bad Request", "text/plain; charset=utf-8", "bad request")
      return null
    }

    return when (requestUri.path) {
      "/callback" -> {
        writeResponse(socket, "200 OK", "text/html; charset=utf-8", FORWARD_PAGE)
        null
      }
      "/finish" -> {
        if (requestUri.getQueryParameter("state") != expectedState) {
          writeResponse(socket, "400 Bad Request", "text/plain; charset=utf-8", "state mismatch")
          null
        } else {
          val idToken = requestUri.getQueryParameter("id_token")
          val error = requestUri.getQueryParameter("error")
          val result = JSObject()
          when {
            idToken != null -> result.put("idToken", idToken)
            error != null -> result.put("error", error)
            else -> result.put("error", "No credential received")
          }
          writeResponse(socket, "200 OK", "text/html; charset=utf-8", donePage(idToken != null))
          result
        }
      }
      else -> {
        writeResponse(socket, "404 Not Found", "text/plain; charset=utf-8", "not found")
        null
      }
    }
  }

  private fun writeResponse(socket: Socket, status: String, contentType: String, body: String) {
    val bodyBytes = body.toByteArray(StandardCharsets.UTF_8)
    val headers = "HTTP/1.1 $status\r\n" +
      "Content-Type: $contentType\r\n" +
      "Content-Length: ${bodyBytes.size}\r\n" +
      "Cache-Control: no-store\r\n" +
      "Connection: close\r\n\r\n"
    socket.getOutputStream().apply {
      write(headers.toByteArray(StandardCharsets.US_ASCII))
      write(bodyBytes)
      flush()
    }
  }

  private fun isActive(flow: ActiveFlow): Boolean = synchronized(stateLock) {
    activeFlow === flow && pendingInvoke === flow.invoke
  }

  private fun finishSetup(flowId: Int, invoke: Invoke, result: JSObject) {
    synchronized(stateLock) {
      if (flowSerial != flowId || pendingInvoke !== invoke) return
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
    try {
      flow.server.close()
    } catch (_: Exception) {
      // Closing is only used to wake accept(); the result is already final.
    }
    invoke.resolve(result)
  }

  private fun errorResult(message: String): JSObject = JSObject().apply {
    put("error", message)
  }

  private fun donePage(success: Boolean): String {
    val message = if (success) {
      "You're signed in. Return to gratefulagents."
    } else {
      "Sign-in was not completed. Return to gratefulagents to try again."
    }
    return """<!doctype html><html><head><meta charset="utf-8"><title>gratefulagents</title></head>
<body style="font-family:system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0">
<p>$message</p><script>history.replaceState(null,"","/done")</script></body></html>"""
  }

  private val FORWARD_PAGE =
    """<!doctype html><html><head><meta charset="utf-8"><title>Signing in…</title></head>
<body style="font-family:system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0">
<p id="status">Completing sign-in…</p><script>
var h=location.hash?location.hash.slice(1):"";
var q=location.search?location.search.slice(1):"";
if(h||q){location.replace("/finish?"+(h||q))}
else{document.getElementById("status").textContent="Sign-in returned no credential. Go back to gratefulagents to try again."}
</script></body></html>"""
}
