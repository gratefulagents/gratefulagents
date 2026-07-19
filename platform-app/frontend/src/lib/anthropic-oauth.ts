import { isTauri } from "./platform";

export interface AnthropicOAuthStart {
  authorizeUrl: string;
}

export interface AnthropicOAuthComplete {
  status: string;
  email?: string | null;
  anthropicOauthJson: string;
}

export async function startAnthropicOAuth(): Promise<AnthropicOAuthStart> {
  if (!isTauri) throw new Error("Claude OAuth is only available in the desktop app");
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<AnthropicOAuthStart>("start_anthropic_oauth");
}

export async function completeAnthropicOAuth(code: string): Promise<AnthropicOAuthComplete> {
  if (!isTauri) throw new Error("Claude OAuth is only available in the desktop app");
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<AnthropicOAuthComplete>("complete_anthropic_oauth", {
    payload: { code },
  });
}
