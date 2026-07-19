// gratefulagents — local CLI credential detection (Tauri desktop only).
//
// Bridges the native `detect_local_credentials` command so the app can offer a
// one-click import of provider credentials that already live on the user's
// machine. On the web build (or any failure) this
// degrades to "no credentials found" so callers can render unconditionally.

import { isTauri } from "./platform";

export type LocalCredentialProvider = "openai" | "anthropic" | "copilot";

export interface LocalCredential {
  /** Backend provider id. */
  provider: LocalCredentialProvider | string;
  /** Short human label for the credential source. */
  label: string;
  /** Absolute path the credential was read from. */
  sourcePath: string;
  /** Best-effort account identifier (email or login); may be null. */
  account: string | null;
  /** Raw credential file contents, forwarded to the backend as auth.json. */
  authJson: string;
}

// Fields of UpdateMyCredentialsRequest that a detected credential maps onto.
export interface CredentialUpdate {
  openaiOauthJson?: string;
  openaiAccountId?: string;
  anthropicOauthJson?: string;
  copilotOauthJson?: string;
}

/**
 * Detects provider credentials written by local CLIs. Returns an empty array on
 * the web build or when nothing is found.
 */
export async function detectLocalCredentials(): Promise<LocalCredential[]> {
  if (!isTauri) return [];
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const creds = await invoke<LocalCredential[]>("detect_local_credentials");
    return Array.isArray(creds) ? creds : [];
  } catch {
    return [];
  }
}

/** Maps a detected credential onto the credentials update request fields. */
export function credentialToUpdate(cred: LocalCredential): CredentialUpdate {
  switch (cred.provider) {
    case "openai":
      return {
        openaiOauthJson: cred.authJson,
        ...(cred.account ? { openaiAccountId: cred.account } : {}),
      };
    case "anthropic":
      return { anthropicOauthJson: cred.authJson };
    case "copilot":
      return { copilotOauthJson: cred.authJson };
    default:
      return {};
  }
}

/** Merges several detected credentials into a single update request payload. */
export function credentialsToUpdate(creds: LocalCredential[]): CredentialUpdate {
  return creds.reduce<CredentialUpdate>(
    (acc, cred) => ({ ...acc, ...credentialToUpdate(cred) }),
    {},
  );
}
