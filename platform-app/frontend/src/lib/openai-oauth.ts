import { isTauri } from "./platform";

export interface OpenAIOAuthStart {
  mode: "browser" | "device" | string;
  authorizeUrl?: string | null;
  userCode?: string | null;
  verificationUri?: string | null;
  interval?: number | null;
}

export type OpenAIOAuthPollStatus = "pending" | "completed" | "expired" | "error";

export interface OpenAIOAuthPollResult {
  status: OpenAIOAuthPollStatus | string;
  email?: string | null;
  accountId?: string | null;
  openaiOauthJson?: string | null;
  error?: string | null;
}

async function invokeTauri<T>(command: string): Promise<T> {
  if (!isTauri) throw new Error("ChatGPT OAuth is only available in the desktop app");
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<T>(command);
}

/** Starts the browser sign-in with a localhost callback. */
export async function startOpenAIOAuth(): Promise<OpenAIOAuthStart> {
  return await invokeTauri<OpenAIOAuthStart>("start_openai_oauth");
}

/** Starts the device-code fallback (no local port required). */
export async function startOpenAIDeviceOAuth(): Promise<OpenAIOAuthStart> {
  return await invokeTauri<OpenAIOAuthStart>("start_openai_device_oauth");
}

export async function pollOpenAIOAuth(): Promise<OpenAIOAuthPollResult> {
  return await invokeTauri<OpenAIOAuthPollResult>("poll_openai_oauth");
}

export async function cancelOpenAIOAuth(): Promise<void> {
  if (!isTauri) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("cancel_openai_oauth");
  } catch {
    // best-effort cleanup
  }
}
