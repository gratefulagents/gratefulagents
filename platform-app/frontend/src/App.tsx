import * as React from "react";
import { BrowserRouter, Routes, Route, Navigate, useLocation, useNavigate } from "react-router-dom";
import { MotionConfig } from "framer-motion";
import { cn } from "@/lib/utils";

import { ApiMonitorSidebar } from "@/components/ApiMonitorSidebar";
import { AppVersionPrompt } from "@/components/AppVersionPrompt";

const ProjectList = React.lazy(() => import("@/components/ProjectList").then((m) => ({ default: m.ProjectList })));
const ProjectDetail = React.lazy(() => import("@/components/ProjectDetail").then((m) => ({ default: m.ProjectDetail })));
const HomeScreen = React.lazy(() => import("@/components/HomeScreen").then((m) => ({ default: m.HomeScreen })));
const AgentRunDetail = React.lazy(() => import("@/components/AgentRunDetail").then((m) => ({ default: m.AgentRunDetail })));
const AgentOpsConsole = React.lazy(() => import("@/components/AgentOpsConsole").then((m) => ({ default: m.AgentOpsConsole })));
const ObservabilityPage = React.lazy(() => import("@/components/ObservabilityPage").then((m) => ({ default: m.ObservabilityPage })));
const SettingsScreen = React.lazy(() => import("@/components/SettingsScreen").then((m) => ({ default: m.SettingsScreen })));
const SharedWithMeList = React.lazy(() => import("@/components/SharedWithMeList").then((m) => ({ default: m.SharedWithMeList })));
const LoginPage = React.lazy(() => import("@/components/LoginPage").then((m) => ({ default: m.LoginPage })));
const OnboardingWizard = React.lazy(() => import("@/components/onboarding/OnboardingWizard").then((m) => ({ default: m.OnboardingWizard })));
const ResourcePage = React.lazy(() => import("@/components/resources/ResourcePage").then((m) => ({ default: m.ResourcePage })));
const LinearProjectDetail = React.lazy(() => import("@/components/LinearProjectDetail").then((m) => ({ default: m.LinearProjectDetail })));
const GitHubRepositoryDetail = React.lazy(() => import("@/components/GitHubRepositoryDetail").then((m) => ({ default: m.GitHubRepositoryDetail })));
const CronDetail = React.lazy(() => import("@/components/CronDetail").then((m) => ({ default: m.CronDetail })));
const SlackAgentDetail = React.lazy(() => import("@/components/SlackAgentDetail").then((m) => ({ default: m.SlackAgentDetail })));
const SecurityOverview = React.lazy(() => import("@/components/SecurityOverview").then((m) => ({ default: m.SecurityOverview })));
const SecurityScanList = React.lazy(() => import("@/components/SecurityScanList").then((m) => ({ default: m.SecurityScanList })));
const SecurityScanConfigList = React.lazy(() => import("@/components/SecurityScanConfigList").then((m) => ({ default: m.SecurityScanConfigList })));
const SecurityLibraryPage = React.lazy(() => import("@/components/SecurityLibraryPage").then((m) => ({ default: m.SecurityLibraryPage })));
const SecurityScanDetail = React.lazy(() => import("@/components/SecurityScanDetail").then((m) => ({ default: m.SecurityScanDetail })));
const SecurityConfigDetail = React.lazy(() => import("@/components/SecurityConfigDetail").then((m) => ({ default: m.SecurityConfigDetail })));
const SecurityFindingDetail = React.lazy(() => import("@/components/SecurityFindingDetail").then((m) => ({ default: m.SecurityFindingDetail })));
const BugReportList = React.lazy(() => import("@/components/BugReportList").then((m) => ({ default: m.BugReportList })));

import { AuthProvider, useAuth } from "@/contexts/AuthContext";
import { OnboardingRedirect } from "@/components/onboarding/OnboardingRedirect";

import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarHeader,
  SidebarInset,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";

import { TitleBar, TitleBarDivider } from "@/components/shell/TitleBar";
import { Breadcrumbs } from "@/components/shell/Breadcrumbs";
import { RedirectPreservingQuery, SectionNotFound } from "@/components/shell/routing";
import { CommandPalette, type PaletteItem } from "@/components/shell/CommandPalette";
import { useGlobalShortcuts, useViewport } from "@/components/shell/shortcuts";
import { ShortcutsOverlay } from "@/components/shell/ShortcutsOverlay";
import { OfflineBanner } from "@/components/shell/OfflineBanner";
import { NavRail, type RailGroup } from "@/components/shell/NavRail";
import { ProjectsPanel } from "@/components/shell/ProjectsPanel";
import { Toaster } from "@/components/ui/toaster";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useProjects } from "@/hooks/useWatchedList";
import { useRecentsTracker } from "@/hooks/useRecents";
import { useRunTabsTracker, runTabsScope } from "@/hooks/useRunTabs";
import { RunTabs } from "@/components/shell/RunTabs";
import { useDesktopUpdateCheck } from "@/hooks/useDesktopUpdateCheck";
import { getRunAttention } from "@/lib/agentOps";
import { Play, FolderKanban as FolderIcon } from "lucide-react";
import {
  useNativeMenuActions,
  useDeepLinks,
  useWindowDragDrop,
  subscribeOsTheme,
  useDockBadge,
} from "@/lib/native";
import { toggleTheme, applyTheme } from "@/lib/theme";
import type { Project as ProjectT, AgentRun as AgentRunT } from "@/rpc/platform/service_pb";

import {
  Home as HomeIcon,
  Radio,
  Activity,
  Blocks,
  Bug,
  Users,
  PanelLeft,
  Shield,
  ShieldCheck,
} from "lucide-react";
import { isTauri, platform } from "@/lib/platform";

// Settings sub-pages are code-split so /settings stays light: each section's
// data is fetched only when its route mounts.
const SettingsLayout = React.lazy(() => import("@/components/settings/SettingsLayout"));
const SettingsConnectionPage = React.lazy(() => import("@/components/settings/ConnectionPage"));
const SettingsCredentialsPage = React.lazy(() => import("@/components/settings/CredentialsPage"));
const SettingsUsagePage = React.lazy(() => import("@/components/settings/UsagePage"));
const SettingsSoulPage = React.lazy(() => import("@/components/settings/SoulPage"));
const SettingsRoleModelsPage = React.lazy(() => import("@/components/settings/RoleModelsPage"));
const SettingsGitIdentityPage = React.lazy(() => import("@/components/settings/GitIdentityPage"));
const SettingsUpdatesPage = React.lazy(() => import("@/components/settings/UpdatesPage"));
const AdminUsersPage = React.lazy(() => import("@/components/admin/AdminUsersPage"));

/**
 * App sidebar: a permanent 48px navigation rail plus a collapsible projects
 * panel. Collapsing the sidebar hides only the panel, so the rail never has
 * to render project rows as indistinguishable icons.
 */
function AppSidebar({
  projects,
  runs,
}: {
  projects: ProjectT[];
  runs: AgentRunT[];
}) {
  const { user, activeWorkspaceId } = useAuth();
  const needsAttention = runs.some((run) => getRunAttention(run).kind !== "none");

  const railGroups = React.useMemo<RailGroup[]>(() => {
    const groups: RailGroup[] = [
      {
        id: "primary",
        items: [
          { to: "/", label: "Home", icon: HomeIcon },
          {
            to: "/runs",
            label: "Agent Ops",
            icon: Radio,
            attention: needsAttention ? { label: "Runs need attention" } : undefined,
          },
          { to: "/observability", label: "Observability", icon: Activity },
          { to: "/bug-reports", label: "Bug Reports", icon: Bug },
        ],
      },
      {
        id: "workspace",
        items: [
          { to: "/shared", label: "Shared", icon: Users },
          { to: "/security", label: "Security", icon: Shield, match: (p) => p.startsWith("/security") },
          { to: "/resources/skills", label: "Resources", icon: Blocks, match: (p) => p.startsWith("/resources") },
        ],
      },
    ];
    if (user?.role === "admin") {
      groups.push({
        id: "admin",
        items: [{ to: "/admin/users", label: "Users", icon: ShieldCheck }],
      });
    }
    return groups;
  }, [needsAttention, user?.role]);

  return (
    <Sidebar
      collapsible="icon"
      className={cn(
        "bg-[color:var(--color-sidebar)] backdrop-blur-[22px] saturate-150",
        "border-r border-sidebar-border",
      )}
    >
      {/* Space for macOS traffic lights — stays clean on iPad too */}
      <SidebarHeader className={cn("pt-safe p-0 drag-region", isTauri ? "min-h-[40px]" : "min-h-[8px]")} />

      <SidebarContent className="no-drag flex-row gap-0 overflow-hidden p-0">
        <NavRail groups={railGroups} />
        <ProjectsPanel projects={projects} runs={runs} workspaceId={activeWorkspaceId} />
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  );
}

function SidebarToggleButton() {
  const { toggleSidebar } = useSidebar();
  return (
    <button
      onClick={toggleSidebar}
      className={cn(
        "no-drag inline-flex items-center justify-center size-10 md:size-[26px] rounded-[6px]",
        "text-muted-foreground hover:text-foreground hover:bg-muted/60",
        "transition-colors duration-[var(--dur-fast)]",
      )}
      aria-label="Toggle sidebar"
      title="Toggle sidebar"
    >
      <PanelLeft className="size-[15px]" />
    </button>
  );
}

function AuthenticatedShell() {
  const navigate = useNavigate();
  const [paletteOpen, setPaletteOpen] = React.useState(false);
  const [shortcutsOpen, setShortcutsOpen] = React.useState(false);
  const { compact, isTouch } = useViewport();
  // Synchronous navigator hint avoids a square-corner flash on macOS while
  // the async Tauri platform() call confirms.
  const [isMac, setIsMac] = React.useState(
    () => typeof navigator !== "undefined" && /Mac/.test(navigator.platform ?? ""),
  );
  const { user, activeWorkspaceId } = useAuth();
  // Run tabs are per workspace + user: run names/namespaces are resource
  // metadata and must not leak across identities sharing a browser profile.
  const tabsScope = runTabsScope(activeWorkspaceId, user?.id);
  useRecentsTracker();
  useRunTabsTracker(tabsScope);
  useDesktopUpdateCheck();

  React.useEffect(() => {
    platform().then((p) => setIsMac(p === "macos"));
  }, []);

  useGlobalShortcuts({
    onOpenPalette: () => setPaletteOpen(true),
    onOpenSettings: () => navigate("/settings"),
    onOpenShortcuts: () => setShortcutsOpen(true),
  });

  // Live palette items: recent runs + projects.
  const { runs } = useAgentRuns();
  const { projects } = useProjects();
  const runningCount = React.useMemo(
    () => runs.filter((r) => r.phase === "Running" || r.phase === "Pending" || r.phase === "Queued").length,
    [runs],
  );
  useDockBadge(runningCount);
  const runLabels = React.useMemo(() => {
    const m = new Map<string, string>();
    for (const r of runs) {
      if (r.displayName) {
        m.set(`/runs/${r.namespace}/${r.name}`, r.displayName);
      }
    }
    return m;
  }, [runs]);
  const paletteExtras = React.useMemo<PaletteItem[]>(() => {
    const items: PaletteItem[] = [];
    for (const r of runs.slice(0, 40)) {
      items.push({
        id: `run.${r.namespace}.${r.name}`,
        group: "Runs",
        label: r.displayName || r.name,
        hint: r.phase || r.repoUrl,
        icon: <Play className="size-4" />,
        keywords: [r.name, r.namespace, r.repoUrl || "", r.workflowMode || ""],
        action: () => navigate(`/runs/${r.namespace}/${r.name}`),
      });
    }
    for (const p of projects.slice(0, 30)) {
      items.push({
        id: `project.${p.namespace}.${p.name}`,
        group: "Projects",
        label: p.displayName || p.name,
        hint: `${p.namespace}/${p.name}`,
        icon: <FolderIcon className="size-4" />,
        keywords: [p.namespace, p.provider || ""],
        action: () => navigate(`/projects/${p.namespace}/${p.name}`),
      });
    }
    return items;
  }, [runs, projects, navigate]);

  // Native menu / tray / global-shortcut relay.
  useNativeMenuActions((action) => {
    switch (action) {
      case "command-palette":
        setPaletteOpen(true);
        break;
      case "settings":
      case "open-diagnostics":
        navigate("/settings");
        break;
      case "new-run":
        navigate("/");
        break;
      case "toggle-theme":
        toggleTheme();
        break;
      case "reload":
        window.location.reload();
        break;
      case "reload-hard":
        window.location.href = window.location.pathname;
        break;
    }
  });

  // Deep-link handler: gratefulagents://run/<namespace>/<name>, gratefulagents://settings.
  useDeepLinks((urls) => {
    for (const raw of urls) {
      try {
        const url = new URL(raw);
        if (url.protocol !== "gratefulagents:") continue;
        const host = url.host || url.pathname.replace(/^\/+/, "").split("/")[0];
        const parts = url.pathname.replace(/^\/+/, "").split("/").filter(Boolean);
        if (host === "settings") {
          navigate("/settings");
          return;
        }
        if (host === "run" && parts.length >= 2) {
          navigate(`/runs/${parts[0]}/${parts[1]}`);
          return;
        }
      } catch {
        // Ignore malformed urls.
      }
    }
  });

  // Optional: when a native drag-drop arrives at the window, broadcast a
  // DOM event so route-level components can consume it without prop drilling.
  useWindowDragDrop({
    onDrop: (paths) => {
      window.dispatchEvent(
        new CustomEvent("gratefulagents:files-dropped", { detail: { paths } }),
      );
    },
  });

  // Follow OS theme changes when the user hasn't pinned a preference.
  React.useEffect(() => {
    let off = () => {};
    void subscribeOsTheme((next) => {
      if (localStorage.getItem("theme")) return;
      applyTheme(next, { persist: false });
    }).then((u) => {
      off = u;
    });
    return () => off();
  }, []);

  return (
    <TooltipProvider>
      {/* Definite percentage height chain from #root (see index.css) — dvh/svh
          are unreliable in WebKitGTK and must not size the app shell. */}
      <SidebarProvider
        defaultOpen={!compact}
        className="h-full min-h-0"
        style={{ "--sidebar-width": "18rem", "--sidebar-width-icon": "3rem" } as React.CSSProperties}
      >
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:top-2 focus:left-2 focus:px-4 focus:py-2 focus:bg-primary focus:text-primary-foreground focus:rounded-md"
        >
          Skip to content
        </a>

        <AppSidebar
          projects={projects}
          runs={runs}
        />

        <SidebarInset
          className={cn(
            "h-full max-h-full overflow-hidden",
            "bg-background",
            isMac && "rounded-tl-[10px]",
          )}
        >
          <TitleBar
            onOpenPalette={() => setPaletteOpen(true)}
            trail={<Breadcrumbs />}
            right={
              <>
                <ApiMonitorSidebar />
                <SidebarToggleButton />
              </>
            }
          />
          <TitleBarDivider />
          <RunTabs runs={runs} scope={tabsScope} />
          <OfflineBanner />

          <main
            id="main-content"
            className={cn(
              "flex-1 overflow-hidden relative",
              isTouch && "pb-safe",
            )}
          >
            <React.Suspense fallback={<RouteFallback />}>
            <Routes>
              <Route path="/" element={<Scroll><HomeScreen /></Scroll>} />
              <Route path="/projects" element={<Scroll><ProjectList /></Scroll>} />
              <Route path="/projects/:namespace/:name" element={<Scroll><ProjectDetail /></Scroll>} />
              <Route path="/shared" element={<Scroll><SharedWithMeList /></Scroll>} />
              <Route path="/runs" element={<Scroll><AgentOpsConsole /></Scroll>} />
              <Route path="/observability" element={<Scroll><ObservabilityPage /></Scroll>} />
              <Route path="/bug-reports" element={<Scroll><BugReportList /></Scroll>} />
              <Route path="/runs/:namespace/:name" element={<AgentRunDetail />} />
              <Route path="/linear" element={<Navigate to="/projects" replace />} />
              <Route path="/linear/:namespace/:name" element={<Scroll><LinearProjectDetail /></Scroll>} />
              <Route path="/github" element={<Navigate to="/projects" replace />} />
              <Route path="/github/:namespace/:name" element={<Scroll><GitHubRepositoryDetail /></Scroll>} />
              <Route path="/cron" element={<Navigate to="/projects" replace />} />
              <Route path="/cron/:namespace/:name" element={<Scroll><CronDetail /></Scroll>} />
              <Route path="/slack" element={<Navigate to="/projects" replace />} />
              <Route path="/slack/:namespace/:name" element={<Scroll><SlackAgentDetail /></Scroll>} />
              <Route path="/security" element={<Scroll><SecurityOverview /></Scroll>} />
              <Route path="/security/runs" element={<Scroll><SecurityScanList /></Scroll>} />
              <Route path="/security/configs" element={<Scroll><SecurityScanConfigList /></Scroll>} />
              <Route path="/security/configs/:namespace/:name" element={<Scroll><SecurityConfigDetail /></Scroll>} />
              <Route path="/security/library" element={<Scroll><SecurityLibraryPage /></Scroll>} />
              {/* Legacy/alias security paths. Redirects keep the query string so
                  filtered bookmarks and notification deep links survive renames. */}
              <Route path="/security/overview" element={<RedirectPreservingQuery to="/security" />} />
              <Route path="/security/scans" element={<RedirectPreservingQuery to="/security/runs" />} />
              <Route path="/security/scan-runs" element={<RedirectPreservingQuery to="/security/runs" />} />
              <Route path="/security/findings" element={<RedirectPreservingQuery to="/security/runs" />} />
              <Route path="/security/configurations" element={<RedirectPreservingQuery to="/security/configs" />} />
              {/* A config link without a namespace can't be resolved client-side;
                  land on the list pre-filtered by that name instead of 404ing. */}
              <Route path="/security/configs/:name" element={<RedirectPreservingQuery to="/security/configs" params={{ q: ":name" }} />} />
              <Route path="/security/library/:tab" element={<RedirectPreservingQuery to="/security/library" params={{ tab: ":tab" }} />} />
              <Route path="/security/:namespace/:runName" element={<Scroll><SecurityScanDetail /></Scroll>} />
              <Route path="/security/:namespace/:runName/findings/:findingId" element={<Scroll><SecurityFindingDetail /></Scroll>} />
              <Route
                path="/security/*"
                element={(
                  <Scroll>
                    <SectionNotFound
                      section="security"
                      links={[
                        { to: "/security", label: "Security overview" },
                        { to: "/security/runs", label: "Scan runs" },
                        { to: "/security/configs", label: "Configurations" },
                        { to: "/security/library", label: "Library" },
                      ]}
                    />
                  </Scroll>
                )}
              />
              <Route path="/settings" element={<Scroll><SettingsLayout /></Scroll>}>
                <Route index element={<SettingsScreen />} />
                <Route path="connection" element={<SettingsConnectionPage />} />
                <Route path="credentials" element={<SettingsCredentialsPage />} />
                <Route path="usage" element={<SettingsUsagePage />} />
                <Route path="soul" element={<SettingsSoulPage />} />
                <Route path="role-models" element={<SettingsRoleModelsPage />} />
                <Route path="git" element={<SettingsGitIdentityPage />} />
                <Route path="updates" element={<SettingsUpdatesPage />} />
              </Route>
              <Route path="/resources" element={<Navigate to="/resources/skills" replace />} />
              <Route path="/resources/:kind" element={<Scroll><ResourcePage /></Scroll>} />
              <Route path="/settings/skills" element={<Navigate to="/resources/skills" replace />} />
              <Route path="/settings/mcp" element={<Navigate to="/resources/mcp-servers" replace />} />
              <Route path="/admin/users" element={<Scroll><AdminUsersPage /></Scroll>} />
            </Routes>
            </React.Suspense>
          </main>
        </SidebarInset>

        <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} extraItems={paletteExtras} runLabels={runLabels} />
        <ShortcutsOverlay open={shortcutsOpen} onOpenChange={setShortcutsOpen} />
        <OnboardingRedirect />
        <Toaster />
      </SidebarProvider>
    </TooltipProvider>
  );
}

function RouteFallback() {
  return (
    <div className="flex h-full items-center justify-center">
      <div
        role="status"
        aria-live="polite"
        className="font-mono text-[12.5px] text-muted-foreground"
      >
        loading…
      </div>
    </div>
  );
}

function Scroll({ children }: { children: React.ReactNode }) {
  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto max-w-[1400px] px-4 py-4 sm:px-6 sm:py-5">{children}</div>
    </div>
  );
}

function AuthenticatedApp() {
  const { isLoading, isAuthenticated } = useAuth();

  if (isLoading) {
    return (
      <div className="flex min-h-full items-center justify-center">
        <div
          role="status"
          aria-live="polite"
          className="text-[12.5px] text-muted-foreground font-mono"
        >
          loading…
        </div>
      </div>
    );
  }
  return (
    <>
      <AppVersionPrompt />
      {!isAuthenticated ? (
        <React.Suspense fallback={<RouteFallback />}>
          <LoginPage />
        </React.Suspense>
      ) : (
        <ShellWithOnboarding />
      )}
    </>
  );
}

/**
 * ShellWithOnboarding swaps the whole app shell for the full-screen first-run
 * wizard on /welcome; everything else renders the normal sidebar shell.
 */
function ShellWithOnboarding() {
  const location = useLocation();
  // A one-time setup link lands on /login?setup_token=…; LoginPage strips the
  // token with history.replaceState, which react-router never observes, so the
  // router can still believe it is on /login after sign-in. Normalize that
  // stale path to Home — otherwise no route matches (blank page) and the
  // first-run onboarding redirect burns its once-per-session decision on a
  // path it never redirects from.
  if (location.pathname === "/login") {
    return <Navigate to="/" replace />;
  }
  if (location.pathname === "/welcome") {
    return (
      <React.Suspense fallback={<RouteFallback />}>
        <OnboardingWizard />
      </React.Suspense>
    );
  }
  return <AuthenticatedShell />;
}

export default function App() {
  return (
    // reducedMotion="user" makes framer-motion honor prefers-reduced-motion;
    // the CSS rule in index.css only covers CSS animations/transitions.
    <MotionConfig reducedMotion="user">
      <BrowserRouter>
        <AuthProvider>
          <AuthenticatedApp />
        </AuthProvider>
      </BrowserRouter>
    </MotionConfig>
  );
}
