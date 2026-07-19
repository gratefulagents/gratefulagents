package dev.gratefulagents.app.mobilenavigation

import android.app.Activity
import android.webkit.WebView
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import app.tauri.annotation.TauriPlugin
import app.tauri.plugin.Plugin

/**
 * Routes Android's system Back action through the WebView's history first.
 *
 * BrowserRouter stores in-app navigation in that same history, so goBack()
 * produces the popstate event React Router expects. At the root route the
 * callback disables itself and delegates to Android's normal task behavior.
 */
@TauriPlugin
class MobileNavigationPlugin(private val activity: Activity) : Plugin(activity) {
  private var backCallback: OnBackPressedCallback? = null

  override fun load(webView: WebView) {
    val componentActivity = activity as? ComponentActivity ?: return

    backCallback?.remove()
    backCallback = object : OnBackPressedCallback(true) {
      override fun handleOnBackPressed() {
        if (webView.canGoBack()) {
          webView.goBack()
          return
        }

        // Do not trap Back at the root. Disabling before delegation prevents
        // this callback from recursively receiving the same event. Re-enable
        // afterward because Android may background rather than destroy the
        // activity; navigation must still work when the user returns.
        isEnabled = false
        try {
          componentActivity.onBackPressedDispatcher.onBackPressed()
        } finally {
          isEnabled = true
        }
      }
    }.also { callback ->
      componentActivity.onBackPressedDispatcher.addCallback(componentActivity, callback)
    }
  }
}
