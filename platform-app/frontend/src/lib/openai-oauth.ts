import { client } from "./client";
import { isTauri } from "./platform";
import type { MyCredentials } from "@/rpc/platform/service_pb";

export interface OpenAIOAuthStart {
  mode: "browser" | "device" | string;
  authorizeUrl?: string | null;
  verificationUri?: string | null;
  userCode?: string | null;
  interval?: number | null;
  sessionId?: string;
}

export type OpenAIOAuthPollStatus = "pending" | "completed" | "expired" | "error";

export interface OpenAIOAuthPollResult {
  status: OpenAIOAuthPollStatus | string;
  email?: string | null;
  accountId?: string | null;
  openaiOauthJson?: string | null;
  credentials?: MyCredentials;
  error?: string | null;
}

async function invokeTauri<T>(command: string): Promise<T> {
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<T>(command);
}

/** Starts localhost PKCE on desktop and a no-port device flow on web. */
export async function startOpenAIOAuth(): Promise<OpenAIOAuthStart> {
  if (!isTauri) {
    const result = await client.startProviderOAuth({ provider: "openai" });
    return {
      mode: result.mode,
      authorizeUrl: result.authorizeUrl,
      verificationUri: result.authorizeUrl,
      userCode: result.userCode,
      interval: result.intervalSeconds,
      sessionId: result.sessionId,
    };
  }
  return await invokeTauri<OpenAIOAuthStart>("start_openai_oauth");
}

/** Starts the device-code flow (no local port required). */
export async function startOpenAIDeviceOAuth(): Promise<OpenAIOAuthStart> {
  if (!isTauri) return await startOpenAIOAuth();
  return await invokeTauri<OpenAIOAuthStart>("start_openai_device_oauth");
}

export async function pollOpenAIOAuth(sessionId?: string): Promise<OpenAIOAuthPollResult> {
  if (!isTauri) {
    const result = await client.pollProviderOAuth({ provider: "openai", sessionId });
    return {
      status: result.status,
      email: result.email || null,
      credentials: result.credentials,
      error: result.error || null,
    };
  }
  return await invokeTauri<OpenAIOAuthPollResult>("poll_openai_oauth");
}

export async function cancelOpenAIOAuth(): Promise<void> {
  if (!isTauri) return;
  try {
    await invokeTauri("cancel_openai_oauth");
  } catch {
    // best-effort cleanup
  }
}
