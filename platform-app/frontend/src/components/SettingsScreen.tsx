import * as React from "react";
import {
  Bug,
  FolderOpen,
  LifeBuoy,
  LogOut,
  Mail,
  Monitor,
  Moon,
  Sun,
  SunMoon,
  UserRound,
} from "lucide-react";

import { SettingsSection } from "@/components/settings-section";
import {
  Avatar,
  AvatarFallback,
  AvatarImage,
} from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { useAuth } from "@/contexts/AuthContext";
import {
  DIAGNOSTICS_EMAIL_URL,
  DIAGNOSTICS_ISSUE_URL,
  openDiagnosticLogs,
  openExternal,
} from "@/lib/native";
import {
  setThemePreference,
  useThemePreference,
  type ThemePreference,
} from "@/lib/theme";

/**
 * General settings (/settings index): appearance, account, and diagnostics.
 * Renders inside SettingsLayout, whose sidebar navigates to every other
 * section — so this page holds only the always-relevant personal basics.
 *
 * Policy: settings is for per-user preferences and personal resources only.
 * New resource-management surfaces (policies, triggers, templates, …) get
 * their own top-level routes — like /slack — never new settings sections.
 */
export function SettingsScreen() {
  return (
    <div className="flex max-w-[760px] flex-col gap-5">
      <header className="pt-1">
        <h1>General</h1>
        <p className="text-[12.5px] text-muted-foreground">
          Appearance, account, and diagnostics.
        </p>
      </header>

      <AppearanceSettings />

      <AccountSettings />

      <DiagnosticsSettings />
    </div>
  );
}

const themeOptions: { value: ThemePreference; label: string; icon: React.ReactNode }[] = [
  { value: "light", label: "Light", icon: <Sun /> },
  { value: "dark", label: "Dark", icon: <Moon /> },
  { value: "system", label: "System", icon: <Monitor /> },
];

function AppearanceSettings() {
  const preference = useThemePreference();
  return (
    <SettingsSection
      icon={<SunMoon />}
      title="Appearance"
      description="Pin a theme, or follow the OS appearance."
    >
      <ToggleGroup
        aria-label="Theme"
        value={[preference]}
        onValueChange={(next) => {
          const value = next[0] as ThemePreference | undefined;
          if (value) setThemePreference(value);
        }}
        variant="outline"
        size="sm"
      >
        {themeOptions.map((option) => (
          <ToggleGroupItem key={option.value} value={option.value} aria-label={option.label}>
            {option.icon}
            {option.label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
    </SettingsSection>
  );
}

function AccountSettings() {
  const { user, logout } = useAuth();
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  const displayName = user?.name || user?.username || "";

  return (
    <SettingsSection
      icon={<UserRound />}
      title="Account"
      description="The identity used for runs, credentials, and sharing."
    >
      <div className="flex items-center gap-3">
        <Avatar className="size-9">
          {user?.picture && <AvatarImage src={user.picture} alt="" />}
          <AvatarFallback>
            {displayName ? displayName.slice(0, 2).toUpperCase() : <UserRound />}
          </AvatarFallback>
        </Avatar>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium tracking-tight">
            {displayName || "—"}
          </div>
          <div className="truncate font-mono text-[11.5px] text-muted-foreground">
            {user?.email || "offline"}
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={() => setConfirmOpen(true)}>
          <LogOut data-icon="inline-start" />
          Sign out
        </Button>
      </div>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="Sign out?"
        description="You'll need to sign in again to use this workspace."
        confirmLabel="Sign out"
        destructive
        onConfirm={() => logout()}
      />
    </SettingsSection>
  );
}

function DiagnosticsSettings() {
  return (
    <SettingsSection
      icon={<LifeBuoy />}
      title="Diagnostics"
      description="Open the app logs, report a bug on GitHub, or email logs privately for support."
    >
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={() => void openDiagnosticLogs()}>
          <FolderOpen data-icon="inline-start" />
          Open logs
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void openExternal(DIAGNOSTICS_ISSUE_URL)}
        >
          <Bug data-icon="inline-start" />
          Report issue
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void openExternal(DIAGNOSTICS_EMAIL_URL)}
        >
          <Mail data-icon="inline-start" />
          Email diagnostic logs
        </Button>
      </div>
    </SettingsSection>
  );
}
