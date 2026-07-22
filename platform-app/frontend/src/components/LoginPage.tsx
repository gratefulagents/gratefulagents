/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from 'react';
import { GoogleLogin, GoogleOAuthProvider } from '@react-oauth/google';
import { useAuth } from '../contexts/AuthContext';
import { isTauri } from '@/lib/platform';
import { ThemeToggle } from '@/components/ThemeToggle';
import { WorkspaceSwitcher } from '@/components/shell/WorkspaceSwitcher';
import { useTheme } from '@/lib/theme';
import { APP_VERSION } from '@/lib/build-info';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

function GoogleIcon() {
  return (
    <svg viewBox="0 0 24 24" className="h-5 w-5" aria-hidden>
      <path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 01-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4" />
      <path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853" />
      <path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05" />
      <path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335" />
    </svg>
  );
}

function decodeJwtPayload(token: string): Record<string, unknown> {
  const payload = token.split('.')[1];
  if (!payload) {
    throw new Error('Google credential is missing a payload');
  }
  const padded = payload.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(payload.length / 4) * 4, '=');
  const decoded = atob(padded);
  const parsed = JSON.parse(decoded) as unknown;
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('Google credential payload is invalid');
  }
  return parsed as Record<string, unknown>;
}

function verifyGoogleNonce(idToken: string, expectedNonce: string): void {
  let payload: Record<string, unknown>;
  try {
    payload = decodeJwtPayload(idToken);
  } catch {
    throw new Error('Unable to verify Google Sign-In nonce');
  }
  if (payload.nonce !== expectedNonce) {
    throw new Error('Google Sign-In nonce mismatch');
  }
}

export function LoginPage() {
  const { connectToServer, environment, error: authError, isConnected, loginWithGoogle, loginWithPassword, config, workspaces } = useAuth();
  const theme = useTheme();
  const [serverUrl, setServerUrl] = useState(environment.endpointUrl);
  const [cfClientId, setCfClientId] = useState(environment.cfAccessClientId);
  const [cfClientSecret, setCfClientSecret] = useState(environment.cfAccessClientSecret);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [showCloudflareAccess, setShowCloudflareAccess] = useState(
    !!(environment.cfAccessClientId || environment.cfAccessClientSecret),
  );
  const usernameInputRef = useRef<HTMLInputElement>(null);
  const [webGoogleNonce] = useState(() => crypto.randomUUID());

  const focusNativeShell = useCallback(async () => {
    if (isTauri) {
      const [{ getCurrentWebviewWindow }, { getCurrentWebview }] = await Promise.all([
        import('@tauri-apps/api/webviewWindow'),
        import('@tauri-apps/api/webview'),
      ]);

      const currentWindow = getCurrentWebviewWindow();
      const currentWebview = getCurrentWebview();

      await Promise.allSettled([
        currentWindow.show(),
        currentWindow.unminimize(),
        currentWindow.setFocus(),
        currentWebview.show(),
        currentWebview.setFocus(),
      ]);
    }

    requestAnimationFrame(() => usernameInputRef.current?.focus());
  }, []);

  useEffect(() => {
    void focusNativeShell();

    const handleWindowFocus = () => {
      void focusNativeShell();
    };

    window.addEventListener('focus', handleWindowFocus);
    return () => window.removeEventListener('focus', handleWindowFocus);
  }, [focusNativeShell]);

  useEffect(() => {
    setServerUrl(environment.endpointUrl);
    setCfClientId(environment.cfAccessClientId);
    setCfClientSecret(environment.cfAccessClientSecret);
    setShowCloudflareAccess(
      !!(environment.cfAccessClientId || environment.cfAccessClientSecret),
    );
  }, [environment]);

  const handleConnect = async () => {
    const endpointUrl = serverUrl.trim();
    if (!endpointUrl) {
      setError('gratefulagents URL is required');
      return;
    }

    setIsLoading(true);
    setError('');
    try {
      await connectToServer({
        endpointUrl,
        cfAccessClientId: cfClientId.trim(),
        cfAccessClientSecret: cfClientSecret.trim(),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Connection failed');
    } finally {
      setIsLoading(false);
    }
  };

  const handleGoogleSuccess = async (credentialResponse: { credential?: string }) => {
    if (!credentialResponse.credential) {
      setError('No credential received from Google');
      return;
    }
    setIsLoading(true);
    setError('');
    try {
      verifyGoogleNonce(credentialResponse.credential, webGoogleNonce);
      await loginWithGoogle(credentialResponse.credential);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed');
    } finally {
      setIsLoading(false);
    }
  };

  const handlePasswordLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username || !password) {
      setError('Username and password are required');
      return;
    }
    setIsLoading(true);
    setError('');
    try {
      await loginWithPassword(username, password);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed');
    } finally {
      setIsLoading(false);
    }
  };

  const handleGoogleLoginTauri = async () => {
    if (!config.googleClientId) return;

    const { invoke } = await import('@tauri-apps/api/core');

    setIsLoading(true);
    setError('');

    const nonce = crypto.randomUUID();
    const params = new URLSearchParams({
      client_id: config.googleClientId,
      redirect_uri: 'http://localhost',
      response_type: 'id_token',
      scope: 'openid email profile',
      nonce,
      prompt: 'select_account',
    });
    const authUrl = `https://accounts.google.com/o/oauth2/v2/auth?${params}`;

    try {
      const { idToken, error: oauthError } = await invoke<{
        idToken: string | null;
        error: string | null;
      }>('plugin:google-oauth|start_google_oauth', { payload: { authUrl } });

      if (oauthError) {
        if (oauthError !== 'cancelled') {
          setError(`Google Sign-In failed: ${oauthError}`);
        }
        return;
      }

      if (idToken) {
        verifyGoogleNonce(idToken, nonce);
        await loginWithGoogle(idToken);
      } else {
        setError('No credential received from Google');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to open Google Sign-In');
    } finally {
      setIsLoading(false);
    }
  };

  const googleEnabled = !!config.googleClientId;

  const content = (
    <div
      className="relative flex min-h-screen items-center justify-center bg-background px-6 text-foreground"
      onPointerDownCapture={() => {
        void focusNativeShell();
      }}
    >
      {/* Titlebar-height drag strip — the login screen renders without the
          app TitleBar, so give the window a grabbable area on desktop. */}
      <div aria-hidden className="drag-region absolute inset-x-0 top-0 h-11" />
      <div className="absolute right-4 top-4">
        <ThemeToggle />
      </div>
      <div className="surface-card w-full max-w-sm rounded-xl border border-border p-8 text-card-foreground shadow-[var(--elevation-mid)]">
        <div className="mb-6 flex justify-center">
          <img src="/logo.png" alt="Grateful Agents" className="h-20 w-20" />
        </div>
        <h1 className="mb-6 text-center text-2xl font-bold text-foreground">Sign In</h1>

        {isTauri && workspaces.length > 1 && (
          <div className="mb-4">
            <WorkspaceSwitcher />
          </div>
        )}

        {isTauri && (
          <div className="mb-6 space-y-3">
            <div>
              <Label htmlFor="gratefulagents-url" className="mb-1 text-sm text-muted-foreground">gratefulagents URL</Label>
              <Input
                id="gratefulagents-url"
                type="url"
                value={serverUrl}
                onChange={e => setServerUrl(e.target.value)}
                placeholder="https://gratefulagents.example.com"
                className="bg-background"
                disabled={isLoading}
              />
            </div>

            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setShowCloudflareAccess(prev => !prev)}
              className="h-auto justify-start px-0 text-sm text-muted-foreground hover:bg-transparent hover:text-foreground"
            >
              {showCloudflareAccess ? 'Hide' : 'Show'} Cloudflare Access
            </Button>

            {showCloudflareAccess && (
              <>
                <div>
                  <Label htmlFor="cf-client-id" className="mb-1 text-sm text-muted-foreground">Cloudflare Client ID</Label>
                  <Input
                    id="cf-client-id"
                    type="text"
                    value={cfClientId}
                    onChange={e => setCfClientId(e.target.value)}
                    placeholder="CF-Access-Client-Id"
                    className="bg-background"
                    disabled={isLoading}
                  />
                </div>
                <div>
                  <Label htmlFor="cf-client-secret" className="mb-1 text-sm text-muted-foreground">Cloudflare Client Secret</Label>
                  <Input
                    id="cf-client-secret"
                    type="password"
                    value={cfClientSecret}
                    onChange={e => setCfClientSecret(e.target.value)}
                    placeholder="CF-Access-Client-Secret"
                    className="bg-background"
                    disabled={isLoading}
                  />
                </div>
              </>
            )}

            <Button
              type="button"
              size="lg"
              onClick={() => {
                void handleConnect();
              }}
              disabled={isLoading}
              className="w-full"
            >
              {isLoading ? 'Connecting...' : isConnected ? 'Reconnect' : 'Connect'}
            </Button>
          </div>
        )}

        {isConnected && (
          <form onSubmit={handlePasswordLogin} className="mb-4 space-y-3">
            <div>
              <Label htmlFor="username" className="mb-1 text-sm text-muted-foreground">Username</Label>
              <Input
                id="username"
                ref={usernameInputRef}
                type="text"
                autoFocus
                autoComplete="username"
                value={username}
                onChange={e => setUsername(e.target.value)}
                placeholder="admin"
                className="bg-background"
                disabled={isLoading}
              />
            </div>
            <div>
              <Label htmlFor="password" className="mb-1 text-sm text-muted-foreground">Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                placeholder="Enter password"
                className="bg-background"
                disabled={isLoading}
              />
            </div>
            <Button
              type="submit"
              size="lg"
              disabled={isLoading}
              className="w-full"
            >
              {isLoading ? 'Signing in...' : 'Sign In'}
            </Button>
          </form>
        )}

        {(error || authError) && (
          <p role="alert" className="mb-4 rounded-lg border border-destructive/25 bg-destructive/12 px-3 py-2 text-sm text-destructive">
            {error || authError}
          </p>
        )}

        {isConnected && googleEnabled && (
          <>
            <div className="mb-4 flex items-center gap-3">
              <div className="h-px flex-1 bg-border" />
              <span className="text-xs text-muted-foreground">OR</span>
              <div className="h-px flex-1 bg-border" />
            </div>
            <div className="flex justify-center">
              {isLoading ? (
                <div className="text-muted-foreground">Signing in...</div>
              ) : isTauri ? (
                <button
                  type="button"
                  onClick={() => void handleGoogleLoginTauri()}
                  className="flex w-full items-center justify-center gap-3 rounded-lg border border-input bg-background px-4 py-2.5 font-medium text-foreground transition-colors hover:bg-muted"
                >
                  <GoogleIcon />
                  Sign in with Google
                </button>
              ) : (
                <GoogleLogin
                  nonce={webGoogleNonce}
                  onSuccess={handleGoogleSuccess}
                  onError={() => setError('Google Sign-In failed')}
                  theme={theme === 'dark' ? 'filled_black' : 'outline'}
                  size="large"
                  width="320"
                />
              )}
            </div>
          </>
        )}

        <p
          className="mt-6 text-center font-mono text-xs text-muted-foreground"
          title={`App version ${APP_VERSION}`}
        >
          build v{APP_VERSION}
        </p>
      </div>
    </div>
  );

  if (googleEnabled && !isTauri) {
    return (
      <GoogleOAuthProvider clientId={config.googleClientId}>
        {content}
      </GoogleOAuthProvider>
    );
  }

  return content;
}
