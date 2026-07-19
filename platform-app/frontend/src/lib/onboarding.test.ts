import { afterEach, describe, expect, it } from "vitest";

import {
  checklistDismissed,
  dismissChecklist,
  dismissOnboarding,
  emptyCredentialPresence,
  hasProviderCredential,
  onboardingDismissed,
  presenceFromServer,
  projectNameFromRepo,
  setupProgress,
  setupStepsDone,
  shouldOfferOnboarding,
  shouldShowChecklist,
} from "@/lib/onboarding";

const empty = emptyCredentialPresence;

afterEach(() => {
  localStorage.clear();
});

describe("presenceFromServer / hasProviderCredential", () => {
  it("maps server flags and treats any provider credential as connected", () => {
    const p = presenceFromServer({
      namespace: "dana-x",
      anthropicApiKeyPresent: false,
      openaiApiKeyPresent: false,
      openrouterApiKeyPresent: false,
      anthropicOauthPresent: true,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: true,
    });
    expect(p.namespace).toBe("dana-x");
    expect(p.anthropicOauth).toBe(true);
    expect(hasProviderCredential(p)).toBe(true);
  });

  it("treats an OpenRouter API key as a provider credential", () => {
    expect(hasProviderCredential({ ...empty, openrouterApiKey: true })).toBe(true);
  });

  it("does not count the GitHub token as a provider credential", () => {
    expect(hasProviderCredential({ ...empty, githubToken: true })).toBe(false);
  });
});

describe("shouldOfferOnboarding", () => {
  const base = { presence: empty, projectCount: 0, role: "member", dismissed: false };

  it("offers onboarding to a brand-new member", () => {
    expect(shouldOfferOnboarding(base)).toBe(true);
  });

  it("never offers when dismissed, a viewer, or the account has state", () => {
    expect(shouldOfferOnboarding({ ...base, dismissed: true })).toBe(false);
    expect(shouldOfferOnboarding({ ...base, role: "viewer" })).toBe(false);
    expect(shouldOfferOnboarding({ ...base, projectCount: 1 })).toBe(false);
    expect(
      shouldOfferOnboarding({ ...base, presence: { ...empty, openaiApiKey: true } }),
    ).toBe(false);
    expect(
      shouldOfferOnboarding({ ...base, presence: { ...empty, githubToken: true } }),
    ).toBe(false);
  });
});

describe("setup progress + checklist visibility", () => {
  it("counts the three steps", () => {
    const progress = setupProgress({ ...empty, copilotOauth: true, githubToken: true }, 0);
    expect(progress).toEqual({ provider: true, github: true, project: false });
    expect(setupStepsDone(progress)).toBe(2);
  });

  it("shows the checklist only while the essentials are missing", () => {
    const missingProvider = setupProgress({ ...empty, githubToken: true }, 3);
    expect(shouldShowChecklist({ progress: missingProvider, role: "member", dismissed: false })).toBe(true);

    // Provider + project present: leave the user alone even without GitHub.
    const essentials = setupProgress({ ...empty, anthropicApiKey: true }, 1);
    expect(shouldShowChecklist({ progress: essentials, role: "member", dismissed: false })).toBe(false);

    expect(shouldShowChecklist({ progress: missingProvider, role: "viewer", dismissed: false })).toBe(false);
    expect(shouldShowChecklist({ progress: missingProvider, role: "member", dismissed: true })).toBe(false);
  });
});

describe("dismissal flags", () => {
  it("scopes wizard and checklist flags per user and keeps them independent", () => {
    expect(onboardingDismissed("u1")).toBe(false);
    dismissOnboarding("u1");
    expect(onboardingDismissed("u1")).toBe(true);
    expect(onboardingDismissed("u2")).toBe(false);
    expect(checklistDismissed("u1")).toBe(false);

    dismissChecklist("u2");
    expect(checklistDismissed("u2")).toBe(true);
    expect(onboardingDismissed("u2")).toBe(false);
  });

  it("falls back to a local key when no user id exists", () => {
    dismissOnboarding(undefined);
    expect(onboardingDismissed(undefined)).toBe(true);
    expect(onboardingDismissed("")).toBe(true);
  });
});

describe("projectNameFromRepo", () => {
  it("derives a DNS-safe name from the repo URL basename", () => {
    expect(projectNameFromRepo("https://github.com/acme/My-Repo.git", "")).toBe("my-repo");
    expect(projectNameFromRepo("git@github.com:acme/widget.git", "")).toBe("widget");
    expect(projectNameFromRepo("https://github.com/acme/repo/", "")).toBe("repo");
  });

  it("prefers the display name and normalizes it", () => {
    expect(projectNameFromRepo("https://github.com/acme/repo", "Hello World!")).toBe("hello-world");
  });

  it("caps the length without a trailing dash and falls back when empty", () => {
    const name = projectNameFromRepo("", "x".repeat(39) + "-y-and-more");
    expect(name.length).toBeLessThanOrEqual(40);
    expect(name.endsWith("-")).toBe(false);
    expect(projectNameFromRepo("", "")).toBe("my-project");
    expect(projectNameFromRepo("", "!!!")).toBe("my-project");
  });
});
