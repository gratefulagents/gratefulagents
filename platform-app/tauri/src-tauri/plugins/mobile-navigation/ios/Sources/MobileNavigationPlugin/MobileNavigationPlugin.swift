import Tauri
import WebKit

/// Uses WKWebView's native interactive navigation gestures rather than
/// synthesizing touch events in React. BrowserRouter receives the resulting
/// history changes and keeps the rendered route in sync.
class MobileNavigationPlugin: Plugin {
  @objc public override func load(webview: WKWebView) {
    webview.allowsBackForwardNavigationGestures = true
  }
}

@_cdecl("init_plugin_mobile_navigation")
func initPlugin() -> Plugin {
  MobileNavigationPlugin()
}
