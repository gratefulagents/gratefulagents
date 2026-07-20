import { client } from "./client";
import { isTauri } from "./platform";
import type { MyCredentials } from "@/rpc/platform/service_pb";

export interface AnthropicOAuthStart {
  authorizeUrl: string;
  sessionId?: string;
}

export interface AnthropicOAuthComplete {
  status: string;
  email?: string | null;
  /** Desktop returns credential JSON for the existing UpdateMyCredentials path. */
  anthropicOauthJson?: string;
  /** Web stores tokens server-side and returns presence only. */
  credentials?: MyCredentials;
}

export async function startAnthropicOAuth(): Promise<AnthropicOAuthStart> {
  if (!isTauri) {
    const result = await client.startProviderOAuth({ provider: "anthropic" });
    return { authorizeUrl: result.authorizeUrl, sessionId: result.sessionId };
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<AnthropicOAuthStart>("start_anthropic_oauth");
}

export async function completeAnthropicOAuth(code: string, sessionId?: string): Promise<AnthropicOAuthComplete> {
  if (!isTauri) {
    const result = await client.completeProviderOAuth({ provider: "anthropic", code, sessionId });
    return {
      status: result.status,
      email: result.email || null,
      credentials: result.credentials,
    };
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<AnthropicOAuthComplete>("complete_anthropic_oauth", {
    payload: { code },
  });
}
