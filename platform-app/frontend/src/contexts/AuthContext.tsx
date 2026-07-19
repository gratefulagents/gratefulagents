/* eslint-disable react-hooks/set-state-in-effect */
import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import { backendBaseUrl } from "@/lib/platform";
import {
  hydrateTokens,
  getAccessToken as getTokenSync,
  getRefreshToken,
  setTokens,
  clearTokens,
  clearWorkspaceSecrets,
  getWorkspaceRefreshToken,
  userStoreKey,
  SESSION_EXPIRED_EVENT,
} from "@/lib/client";
import { storeGet, storeSet } from "@/lib/secure-store";
import {
  getAppEnvironment,
  getCloudflareAccessHeaders,
  hydrateAppEnvironment,
  isBackendConfigured,
  saveAppEnvironment,
  type AppEnvironment,
} from "@/lib/app-environment";
import {
  addWorkspace as addWorkspaceStore,
  deriveWorkspaceName,
  getActiveWorkspaceId,
  getWorkspaces,
  removeWorkspace as removeWorkspaceStore,
  setActiveWorkspace,
  updateWorkspace,
  type Workspace,
} from "@/lib/workspaces";
import { runtimeFetch } from "@/lib/runtime-fetch";

function baseUrlForWorkspace(workspace: Workspace): string {
  return workspace.endpointUrl.trim().replace(/\/$/, "");
}

function cloudflareHeadersForWorkspace(workspace: Workspace): Record<string, string> {
  const headers: Record<string, string> = {};
  if (workspace.cfAccessClientId) {
    headers["CF-Access-Client-Id"] = workspace.cfAccessClientId;
  }
  if (workspace.cfAccessClientSecret) {
    headers["CF-Access-Client-Secret"] = workspace.cfAccessClientSecret;
  }
  return headers;
}

interface AuthUser {
  id: string;
  username: string;
  email: string;
  name: string;
  picture: string;
  role: string;
}

interface AppConfig {
  authEnabled: boolean;
  googleClientId: string;
}

interface AuthState {
  isAuthenticated: boolean;
  isLoading: boolean;
  isConnected: boolean;
  accessToken: string | null;
  user: AuthUser | null;
  config: AppConfig;
  environment: AppEnvironment;
  workspaces: Workspace[];
  activeWorkspaceId: string;
  error: string | null;
}

interface AuthContextType extends AuthState {
  connectToServer: (environment: AppEnvironment) => Promise<void>;
  addWorkspace: (environment: AppEnvironment, name?: string) => Promise<void>;
  switchWorkspace: (id: string) => Promise<void>;
  removeWorkspace: (id: string) => Promise<void>;
  renameWorkspace: (id: string, name: string) => Promise<void>;
  loginWithGoogle: (googleIdToken: string) => Promise<void>;
  loginWithPassword: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  getAccessToken: () => string | null;
}

const AuthContext = createContext<AuthContextType | null>(null);

export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({
    isAuthenticated: false,
    isLoading: true,
    isConnected: false,
    accessToken: null,
    user: null,
    config: { authEnabled: true, googleClientId: "" },
    environment: getAppEnvironment(),
    workspaces: getWorkspaces(),
    activeWorkspaceId: getActiveWorkspaceId(),
    error: null,
  });

  const loadConfig = useCallback(async (isInitialLoad: boolean) => {
    if (isInitialLoad) {
      await hydrateAppEnvironment();
      await hydrateTokens();
    }

    const token = getTokenSync();
    const user = (await storeGet<AuthUser>(userStoreKey())) ?? null;
    const environment = getAppEnvironment();

    let config: AppConfig = { authEnabled: true, googleClientId: "" };
    let isConnected = false;
    let error: string | null = null;

    if (isBackendConfigured()) {
      try {
        // A couple of quick retries: the backend can sit behind a proxy that
        // briefly answers 424/5xx while it restarts. Don't fail the whole
        // boot (or show the connect screen) for one blip.
        let lastError: unknown = null;
        let c: Partial<AppConfig> | null = null;
        for (let attempt = 0; attempt < 3 && c === null; attempt++) {
          if (attempt > 0) {
            await new Promise((resolve) => setTimeout(resolve, 400 * attempt));
          }
          try {
            const resp = await runtimeFetch(`${backendBaseUrl()}/api/config`, {
              headers: getCloudflareAccessHeaders(),
            });
            if (!resp.ok) {
              throw new Error(`Server returned HTTP ${resp.status}`);
            }
            c = (await resp.json()) as Partial<AppConfig>;
          } catch (err) {
            lastError = err;
          }
        }
        if (c === null) {
          throw lastError instanceof Error ? lastError : new Error("Cannot reach server");
        }
        config = {
          authEnabled: c.authEnabled ?? true,
          googleClientId: c.googleClientId ?? "",
        };
        isConnected = true;
      } catch (err) {
        error = err instanceof Error ? err.message : "Cannot reach server";
      }
    }

    setState({
      isLoading: false,
      isAuthenticated: isConnected && !!(token && user),
      isConnected,
      accessToken: token,
      user: isConnected ? user : null,
      config,
      environment,
      workspaces: getWorkspaces(),
      activeWorkspaceId: getActiveWorkspaceId(),
      error,
    });
  }, []);

  useEffect(() => {
    void loadConfig(true);
  }, [loadConfig]);

  // Soft sign-out: fired when a token refresh is definitively rejected
  // (refresh token expired/revoked). Swaps to the sign-in screen in place
  // instead of a hard reload, so nothing else in the app is destroyed.
  useEffect(() => {
    function onSessionExpired() {
      setState((prev) => ({
        ...prev,
        isAuthenticated: false,
        accessToken: null,
        user: null,
      }));
    }
    window.addEventListener(SESSION_EXPIRED_EVENT, onSessionExpired);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onSessionExpired);
  }, []);

  const connectToServer = useCallback(async (environment: AppEnvironment) => {
    setState((prev) => ({
      ...prev,
      isLoading: true,
      isConnected: false,
      config: { authEnabled: true, googleClientId: "" },
      error: null,
    }));
    await saveAppEnvironment(environment);
    await hydrateTokens();
    await loadConfig(false);
  }, [loadConfig]);

  const addWorkspace = useCallback(
    async (environment: AppEnvironment, name?: string) => {
      addWorkspaceStore({
        name: name?.trim() || deriveWorkspaceName(environment.endpointUrl),
        endpointUrl: environment.endpointUrl.trim(),
        cfAccessClientId: environment.cfAccessClientId.trim(),
        cfAccessClientSecret: environment.cfAccessClientSecret.trim(),
      });
      setState((prev) => ({
        ...prev,
        isLoading: true,
        isConnected: false,
        accessToken: null,
        user: null,
        config: { authEnabled: true, googleClientId: "" },
        error: null,
      }));
      await hydrateTokens();
      await loadConfig(false);
    },
    [loadConfig],
  );

  const switchWorkspace = useCallback(
    async (id: string) => {
      if (id === getActiveWorkspaceId()) return;
      if (!setActiveWorkspace(id)) return;
      setState((prev) => ({
        ...prev,
        isLoading: true,
        isConnected: false,
        accessToken: null,
        user: null,
        config: { authEnabled: true, googleClientId: "" },
        error: null,
        activeWorkspaceId: id,
        environment: getAppEnvironment(),
      }));
      await hydrateTokens();
      await loadConfig(false);
    },
    [loadConfig],
  );

  const removeWorkspace = useCallback(
    async (id: string) => {
      const workspace = getWorkspaces().find((ws) => ws.id === id);
      const refresh = await getWorkspaceRefreshToken(id);
      if (workspace && refresh) {
        await runtimeFetch(`${baseUrlForWorkspace(workspace)}/auth.v1.AuthService/Logout`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            ...cloudflareHeadersForWorkspace(workspace),
          },
          body: JSON.stringify({ refreshToken: refresh }),
        }).catch(() => {});
      }
      await clearWorkspaceSecrets(id);
      removeWorkspaceStore(id);
      setState((prev) => ({
        ...prev,
        isLoading: true,
        isConnected: false,
        accessToken: null,
        user: null,
        config: { authEnabled: true, googleClientId: "" },
        error: null,
        workspaces: getWorkspaces(),
        activeWorkspaceId: getActiveWorkspaceId(),
        environment: getAppEnvironment(),
      }));
      await hydrateTokens();
      await loadConfig(false);
    },
    [loadConfig],
  );

  const renameWorkspace = useCallback(async (id: string, name: string) => {
    updateWorkspace(id, { name: name.trim() });
    setState((prev) => ({
      ...prev,
      workspaces: getWorkspaces(),
      environment: getAppEnvironment(),
    }));
  }, []);

  const loginWithGoogle = useCallback(async (googleIdToken: string) => {
    const resp = await runtimeFetch(`${backendBaseUrl()}/auth.v1.AuthService/Login`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...getCloudflareAccessHeaders(),
      },
      body: JSON.stringify({ googleIdToken }),
    });
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({ message: "Login failed" }));
      throw new Error(err.message || "Login failed");
    }
    const data = (await resp.json()) as {
      accessToken: string;
      refreshToken: string;
      user: AuthUser;
    };
    await setTokens(data.accessToken, data.refreshToken);
    await storeSet(userStoreKey(), data.user);
    setState((prev) => ({
      ...prev,
      isAuthenticated: true,
      accessToken: data.accessToken,
      user: data.user,
      error: null,
    }));
  }, []);

  const loginWithPassword = useCallback(async (username: string, password: string) => {
    const resp = await runtimeFetch(`${backendBaseUrl()}/auth.v1.AuthService/Login`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...getCloudflareAccessHeaders(),
      },
      body: JSON.stringify({ username, password }),
    });
    if (!resp.ok) {
      const err = await resp.json().catch(() => ({ message: "Login failed" }));
      throw new Error(err.message || "Login failed");
    }
    const data = (await resp.json()) as {
      accessToken: string;
      refreshToken: string;
      user: AuthUser;
    };
    await setTokens(data.accessToken, data.refreshToken);
    await storeSet(userStoreKey(), data.user);
    setState((prev) => ({
      ...prev,
      isAuthenticated: true,
      accessToken: data.accessToken,
      user: data.user,
      error: null,
    }));
  }, []);

  const logout = useCallback(async () => {
    const refresh = getRefreshToken();
    if (refresh) {
      await runtimeFetch(`${backendBaseUrl()}/auth.v1.AuthService/Logout`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...getCloudflareAccessHeaders(),
        },
        body: JSON.stringify({ refreshToken: refresh }),
      }).catch(() => {});
    }
    await clearTokens();
    setState((prev) => ({
      ...prev,
      isAuthenticated: false,
      accessToken: null,
      user: null,
    }));
  }, []);

  const getAccessToken = useCallback(() => state.accessToken, [state.accessToken]);

  return (
    <AuthContext.Provider
      value={{
        ...state,
        connectToServer,
        addWorkspace,
        switchWorkspace,
        removeWorkspace,
        renameWorkspace,
        loginWithGoogle,
        loginWithPassword,
        logout,
        getAccessToken,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}
