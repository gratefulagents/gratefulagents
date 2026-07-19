import { isTauri } from "./platform";

export interface CopilotOAuthStart {
  deviceCode: string;
  userCode: string;
  verificationUri: string;
  verificationUriComplete?: string | null;
  expiresAt: number;
  interval: number;
}

export type CopilotOAuthPollStatus = "pending" | "completed" | "expired" | "denied" | "error";

export interface CopilotOAuthPollResult {
  status: CopilotOAuthPollStatus | string;
  interval?: number | null;
  login?: string | null;
  copilotOauthJson?: string | null;
  error?: string | null;
}

export async function startCopilotOAuth(): Promise<CopilotOAuthStart> {
  if (!isTauri) throw new Error("Copilot OAuth is only available in the desktop app");
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<CopilotOAuthStart>("start_copilot_oauth");
}

export async function pollCopilotOAuth(deviceCode: string): Promise<CopilotOAuthPollResult> {
  if (!isTauri) throw new Error("Copilot OAuth is only available in the desktop app");
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<CopilotOAuthPollResult>("poll_copilot_oauth", {
    payload: { deviceCode },
  });
}
