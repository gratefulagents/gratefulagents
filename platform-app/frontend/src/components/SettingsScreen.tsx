import * as React from "react";
import { Link } from "react-router-dom";
import {
  Bot,
  Bug,
  ChevronRight,
  DownloadCloud,
  FolderOpen,
  GitCommitHorizontal,
  KeyRound,
  LifeBuoy,
  LogOut,
  Mail,
  Monitor,
  Moon,
  Server,
  Sparkles,
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
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { useAuth } from "@/contexts/AuthContext";
import {
  DIAGNOSTICS_EMAIL_URL,
  DIAGNOSTICS_ISSUE_URL,
  openDiagnosticLogs,
  openExternal,
} from "@/lib/native";
import { isTauri } from "@/lib/platform";
import {
  setThemePreference,
  useThemePreference,
  type ThemePreference,
} from "@/lib/theme";

/**
 * Settings hub (/settings): lightweight personal preferences (appearance,
 * account, diagnostics) plus links to lazily loaded sub-pages that fetch
 * only when visited (/settings/connection, /credentials, /skills, /soul,
 * /role-models, /git).
 *
 * Policy: settings is for per-user preferences and personal resources only.
 * New resource-management surfaces (policies, triggers, templates, …) get
 * their own top-level routes — like /slack — never new settings sections.
 */
export function SettingsScreen() {
  const sections: SettingsLinkCardProps[] = [
    ...(isTauri
      ? [
          {
            to: "/settings/connection",
            icon: <Server />,
            title: "Connection",
            description: "Backend endpoint, Cloudflare Access, and workspaces on this device.",
          },
          {
            to: "/settings/updates",
            icon: <DownloadCloud />,
            title: "Desktop updates",
            description: "Keep the desktop app up to date — GitHub token and update checks.",
          },
        ]
      : []),
    {
      to: "/settings/credentials",
      icon: <KeyRound />,
      title: "Credentials",
      description: "API keys and OAuth sign-ins your runs can use.",
    },
    {
      to: "/settings/soul",
      icon: <Sparkles />,
      title: "SOUL",
      description: "Your agent persona — teammates can ask it for your perspective.",
    },
    {
      to: "/settings/role-models",
      icon: <Bot />,
      title: "Role models",
      description: "Personal model overrides for each platform role and provider.",
    },
    {
      to: "/settings/git",
      icon: <GitCommitHorizontal />,
      title: "Git identity",
      description: "The name and email your runs use to author commits.",
    },
  ];

  return (
    <div className="mx-auto flex max-w-[760px] flex-col gap-5 pb-10">
      <header className="pt-1">
        <h1>Settings</h1>
        <p className="text-[12.5px] text-muted-foreground">
          Appearance, workspaces, credentials, and account.
        </p>
      </header>

      <AppearanceSettings />

      <nav aria-label="Settings sections">
        <ItemGroup className="gap-2.5">
          {sections.map((section) => (
            <SettingsLinkCard key={section.to} {...section} />
          ))}
        </ItemGroup>
      </nav>

      <AccountSettings />

      <DiagnosticsSettings />
    </div>
  );
}

interface SettingsLinkCardProps {
  to: string;
  icon: React.ReactNode;
  title: string;
  description: string;
}

/** Card-style link to a settings sub-page; mirrors SettingsSection's header. */
function SettingsLinkCard({ to, icon, title, description }: SettingsLinkCardProps) {
  return (
    <Item render={<Link to={to} />} className="surface-card p-4 sm:p-5">
      <ItemMedia variant="icon">{icon}</ItemMedia>
      <ItemContent className="min-w-0">
        <ItemTitle>{title}</ItemTitle>
        <ItemDescription>{description}</ItemDescription>
      </ItemContent>
      <ItemActions>
        <ChevronRight className="size-4 text-muted-foreground transition-colors duration-[var(--dur-fast)] group-hover/item:text-foreground" />
      </ItemActions>
    </Item>
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
