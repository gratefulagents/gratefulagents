import SwiftRs
import Tauri
import UIKit
import WebKit

class StartGoogleOauthArgs: Decodable {
  let authUrl: String
}

// Modal WKWebView that loads Google's consent page and intercepts the
// redirect to http://localhost to extract the id_token from the URL fragment.
class OAuthWebViewController: UIViewController, WKNavigationDelegate {
  var authUrl: URL!
  var onResult: (([String: Any]) -> Void)?

  private var webView: WKWebView!
  private var didResolve = false

  override func viewDidLoad() {
    super.viewDidLoad()
    view.backgroundColor = .systemBackground

    let config = WKWebViewConfiguration()
    // Use a fresh, non-persistent session so account selection always shows.
    config.websiteDataStore = .nonPersistent()

    webView = WKWebView(frame: view.bounds, configuration: config)
    // Google rejects embedded-webview user agents (403 disallowed_useragent),
    // so present as Mobile Safari.
    webView.customUserAgent =
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1"
    webView.autoresizingMask = [.flexibleWidth, .flexibleHeight]
    webView.navigationDelegate = self
    view.addSubview(webView)

    webView.load(URLRequest(url: authUrl))
  }

  @objc func cancelTapped() {
    finish(["error": "cancelled"])
  }

  func webView(
    _ webView: WKWebView,
    decidePolicyFor navigationAction: WKNavigationAction,
    decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
  ) {
    if let url = navigationAction.request.url, isRedirect(url) {
      decisionHandler(.cancel)
      finish(parseRedirect(url))
      return
    }
    decisionHandler(.allow)
  }

  private func isRedirect(_ url: URL) -> Bool {
    guard url.scheme == "http" else { return false }
    let host = url.host ?? ""
    return host == "localhost" || host == "127.0.0.1"
  }

  private func parseRedirect(_ url: URL) -> [String: Any] {
    var idToken: String?
    var error: String?

    // The ID token arrives in the URL fragment (#id_token=...).
    if let fragment = url.fragment {
      for pair in fragment.components(separatedBy: "&") {
        guard let eq = pair.firstIndex(of: "=") else { continue }
        let key = String(pair[..<eq])
        let value = String(pair[pair.index(after: eq)...]).removingPercentEncoding
        if key == "id_token" {
          idToken = value
        } else if key == "error" {
          error = value
        }
      }
    }

    // Google may also send errors as query parameters.
    if idToken == nil, error == nil,
      let comps = URLComponents(url: url, resolvingAgainstBaseURL: false) {
      error = comps.queryItems?.first(where: { $0.name == "error" })?.value
    }

    var out: [String: Any] = [:]
    if let idToken = idToken {
      out["idToken"] = idToken
    }
    if let error = error {
      out["error"] = error
    } else if idToken == nil {
      out["error"] = "No credential received"
    }
    return out
  }

  private func finish(_ result: [String: Any]) {
    if didResolve { return }
    didResolve = true
    DispatchQueue.main.async { [weak self] in
      guard let self = self else { return }
      self.dismiss(animated: true) {
        self.onResult?(result)
      }
    }
  }
}

class GoogleOauthPlugin: Plugin {
  @objc public func startGoogleOauth(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(StartGoogleOauthArgs.self)

    guard let url = Self.validateAuthUrl(args.authUrl) else {
      invoke.resolve(["error": "InvalidUrl"])
      return
    }

    DispatchQueue.main.async {
      guard let presenter = Self.topViewController() else {
        invoke.resolve(["error": "No view controller available to present sign-in"])
        return
      }

      let controller = OAuthWebViewController()
      controller.authUrl = url
      controller.title = "Sign in with Google"
      controller.onResult = { result in
        invoke.resolve(result)
      }
      controller.navigationItem.leftBarButtonItem = UIBarButtonItem(
        barButtonSystemItem: .cancel,
        target: controller,
        action: #selector(OAuthWebViewController.cancelTapped)
      )

      let nav = UINavigationController(rootViewController: controller)
      nav.modalPresentationStyle = .fullScreen
      presenter.present(nav, animated: true)
    }
  }

  private static func topViewController() -> UIViewController? {
    let keyWindow = UIApplication.shared.connectedScenes
      .compactMap { $0 as? UIWindowScene }
      .flatMap { $0.windows }
      .first { $0.isKeyWindow }

    var top = keyWindow?.rootViewController
    while let presented = top?.presentedViewController {
      top = presented
    }
    return top
  }

  private static func validateAuthUrl(_ raw: String) -> URL? {
    guard
      let components = URLComponents(string: raw),
      components.scheme == "https",
      components.host == "accounts.google.com",
      let url = components.url
    else {
      return nil
    }
    return url
  }
}

@_cdecl("init_plugin_google_oauth")
func initPlugin() -> Plugin {
  return GoogleOauthPlugin()
}
