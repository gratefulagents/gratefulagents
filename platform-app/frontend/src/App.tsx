import * as React from "react";
import { BrowserRouter, Routes, Route, Link, Navigate, useLocation, useNavigate } from "react-router-dom";
import { MotionConfig } from "framer-motion";
import { cn } from "@/lib/utils";

import { ProjectTree } from "@/components/shell/ProjectTree";
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
const LinearProjectList = React.lazy(() => import("@/components/LinearProjectList").then((m) => ({ default: m.LinearProjectList })));
const LinearProjectDetail = React.lazy(() => import("@/components/LinearProjectDetail").then((m) => ({ default: m.LinearProjectDetail })));
const GitHubRepositoryList = React.lazy(() => import("@/components/GitHubRepositoryList").then((m) => ({ default: m.GitHubRepositoryList })));
const GitHubRepositoryDetail = React.lazy(() => import("@/components/GitHubRepositoryDetail").then((m) => ({ default: m.GitHubRepositoryDetail })));
const CronList = React.lazy(() => import("@/components/CronList").then((m) => ({ default: m.CronList })));
const CronDetail = React.lazy(() => import("@/components/CronDetail").then((m) => ({ default: m.CronDetail })));
const SlackAgentsPage = React.lazy(() => import("@/components/SlackAgentSection").then((m) => ({ default: m.SlackAgentsPage })));
const SlackAgentDetail = React.lazy(() => import("@/components/SlackAgentDetail").then((m) => ({ default: m.SlackAgentDetail })));

import { AuthProvider, useAuth } from "@/contexts/AuthContext";
import { OnboardingRedirect } from "@/components/onboarding/OnboardingRedirect";

import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuItem,
  SidebarMenuButton,
  SidebarInset,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";

import { TitleBar, TitleBarDivider } from "@/components/shell/TitleBar";
import { Breadcrumbs } from "@/components/shell/Breadcrumbs";
import { CommandPalette, type PaletteItem } from "@/components/shell/CommandPalette";
import { useGlobalShortcuts, useViewport } from "@/components/shell/shortcuts";
import { ShortcutsOverlay } from "@/components/shell/ShortcutsOverlay";
import { OfflineBanner } from "@/components/shell/OfflineBanner";
import { WorkspaceSwitcher } from "@/components/shell/WorkspaceSwitcher";
import { Toaster } from "@/components/ui/toaster";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useProjects } from "@/hooks/useWatchedList";
import { useRecentsTracker } from "@/hooks/useRecents";
import { useDesktopUpdateCheck } from "@/hooks/useDesktopUpdateCheck";
import { writeLastProject } from "@/lib/lastProject";
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
  Users,
  PanelLeft,
  Plus,
  Settings as SettingsIcon,
  ShieldCheck,
} from "lucide-react";
import { isTauri, platform } from "@/lib/platform";
import { APP_VERSION } from "@/lib/build-info";

// Settings sub-pages are code-split so /settings stays light: each section's
// data is fetched only when its route mounts.
const SettingsLayout = React.lazy(() => import("@/components/settings/SettingsLayout"));
const SettingsConnectionPage = React.lazy(() => import("@/components/settings/ConnectionPage"));
const SettingsCredentialsPage = React.lazy(() => import("@/components/settings/CredentialsPage"));
const SettingsSoulPage = React.lazy(() => import("@/components/settings/SoulPage"));
const SettingsRoleModelsPage = React.lazy(() => import("@/components/settings/RoleModelsPage"));
const SettingsGitIdentityPage = React.lazy(() => import("@/components/settings/GitIdentityPage"));
const SettingsUpdatesPage = React.lazy(() => import("@/components/settings/UpdatesPage"));
const AdminUsersPage = React.lazy(() => import("@/components/admin/AdminUsersPage"));

function AppSidebar({
  projects,
  runs,
}: {
  projects: ProjectT[];
  runs: AgentRunT[];
}) {
  const location = useLocation();
  const { user, activeWorkspaceId } = useAuth();
  const navigate = useNavigate();

  return (
    <Sidebar
      collapsible="icon"
      className={cn(
        "bg-[color:var(--color-sidebar)] backdrop-blur-[22px] saturate-150",
        "border-r border-sidebar-border",
      )}
    >
      {/* Space for macOS traffic lights — stays clean on iPad too */}
      <SidebarHeader className="min-h-[44px] pt-safe px-2 flex items-center gap-2 drag-region">
        <div className="hidden md:block pl-[66px]" aria-hidden />
      </SidebarHeader>

      <SidebarContent className="px-1 no-drag">
        <SidebarGroup>
          <SidebarGroupContent>
            {isTauri && <WorkspaceSwitcher />}
            <div
              className={cn(
                "mt-1 px-2 truncate font-mono text-[10px] text-muted-foreground/60",
                "group-data-[collapsible=icon]:hidden",
              )}
              title={`App version ${APP_VERSION}`}
            >
              build v{APP_VERSION}
            </div>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton
                  render={<Link to="/" />}
                  isActive={location.pathname === "/"}
                  tooltip="Home"
                  className="h-[30px] text-[12.5px] rounded-[6px] px-2 gap-2 data-[active=true]:bg-sidebar-accent hover:bg-sidebar-accent"
                >
                  <HomeIcon className="size-[15px] text-muted-foreground" />
                  <span className="tracking-tight">Home</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
              <SidebarMenuItem>
                <SidebarMenuButton
                  render={<Link to="/runs" />}
                  isActive={location.pathname === "/runs"}
                  tooltip="Agent Ops"
                  className="h-[30px] text-[12.5px] rounded-[6px] px-2 gap-2 data-[active=true]:bg-sidebar-accent hover:bg-sidebar-accent"
                >
                  <Radio className="size-[15px] text-muted-foreground" />
                  <span className="tracking-tight">Agent Ops</span>
                  {runs.some((run) => getRunAttention(run).kind !== "none") && (
                    <span className="ml-auto size-1.5 rounded-full bg-amber-500" aria-label="Runs need attention" />
                  )}
                </SidebarMenuButton>
              </SidebarMenuItem>
              <SidebarMenuItem>
                <SidebarMenuButton
                  render={<Link to="/observability" />}
                  isActive={location.pathname === "/observability"}
                  tooltip="Observability"
                  className="h-[30px] text-[12.5px] rounded-[6px] px-2 gap-2 data-[active=true]:bg-sidebar-accent hover:bg-sidebar-accent"
                >
                  <Activity className="size-[15px] text-muted-foreground" />
                  <span className="tracking-tight">Observability</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel className="text-[10.5px] tracking-[0.08em] font-medium text-muted-foreground/70 flex items-center justify-between pr-1">
            <span>Projects</span>
            <Link to="/projects" className="text-muted-foreground/60 hover:text-foreground" title="All projects">
              <Plus className="size-3.5" />
            </Link>
          </SidebarGroupLabel>
          <SidebarGroupContent>
            <ProjectTree
              key={activeWorkspaceId}
              projects={projects}
              runs={runs}
              workspaceId={activeWorkspaceId}
              onNewChat={(p) => { writeLastProject(p); navigate("/"); }}
            />
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel className="text-[10.5px] tracking-[0.08em] font-medium text-muted-foreground/70">
            Workspace
          </SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton render={<Link to="/shared" />} isActive={location.pathname === "/shared"} tooltip="Shared" className="h-[30px] rounded-[6px] px-2 text-[12.5px] gap-2 hover:bg-sidebar-accent data-[active=true]:bg-sidebar-accent">
                  <Users className="size-[15px] text-muted-foreground" />
                  <span className="tracking-tight">Shared</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
              <SidebarMenuItem>
                <SidebarMenuButton render={<Link to="/resources/skills" />} isActive={location.pathname.startsWith("/resources")} tooltip="Resources" className="h-[30px] rounded-[6px] px-2 text-[12.5px] gap-2 hover:bg-sidebar-accent data-[active=true]:bg-sidebar-accent">
                  <Blocks className="size-[15px] text-muted-foreground" />
                  <span className="tracking-tight">Resources</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {user?.role === "admin" && (
          <SidebarGroup>
            <SidebarGroupLabel className="text-[10.5px] tracking-[0.08em] font-medium text-muted-foreground/70">
              Admin
            </SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton
                    render={<Link to="/admin/users" />}
                    isActive={location.pathname === "/admin/users"}
                    tooltip="Users"
                    className={cn(
                      "h-[30px] text-[12.5px] rounded-[6px] px-2 gap-2",
                      "transition-colors duration-[var(--dur-fast)]",
                      "data-[active=true]:bg-[color:var(--color-primary)]/12",
                      "data-[active=true]:text-foreground",
                      "data-[active=true]:ring-1 data-[active=true]:ring-inset",
                      "data-[active=true]:ring-[color:var(--color-primary)]/20",
                      "hover:bg-sidebar-accent",
                    )}
                  >
                    <ShieldCheck
                      className={cn(
                        "size-[15px]",
                        location.pathname === "/admin/users"
                          ? "text-[color:var(--color-primary)]"
                          : "text-muted-foreground",
                      )}
                    />
                    <span className="tracking-tight">Users</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>

      <SidebarFooter className="px-2 pb-3 pb-safe no-drag">
        <Link
          to="/settings"
          className={cn(
            "flex items-center gap-2 px-1 py-1.5 rounded-[7px]",
            "hover:bg-sidebar-accent transition-colors duration-[var(--dur-fast)]",
            location.pathname.startsWith("/settings") && "bg-sidebar-accent",
          )}
          title="Settings"
        >
          <div className="size-[22px] rounded-full bg-gradient-to-br from-[oklch(0.6_0.12_262)] to-[oklch(0.4_0.1_262)] ring-1 ring-inset ring-border/70 overflow-hidden">
            {user?.picture && (
              <img src={user.picture} alt="" className="size-full object-cover" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[12px] font-medium truncate tracking-tight">
              {user?.name || user?.username || "—"}
            </div>
            <div className="text-[10.5px] text-muted-foreground/80 truncate font-mono">
              {user?.email || "offline"}
            </div>
          </div>
          <SettingsIcon className="size-[15px] text-muted-foreground shrink-0" />
        </Link>
      </SidebarFooter>
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
  useRecentsTracker();
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
      <SidebarProvider defaultOpen={!compact} className="h-full min-h-0">
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
              <Route path="/runs/:namespace/:name" element={<AgentRunDetail />} />
              <Route path="/linear" element={<Scroll><LinearProjectList /></Scroll>} />
              <Route path="/linear/:namespace/:name" element={<Scroll><LinearProjectDetail /></Scroll>} />
              <Route path="/github" element={<Scroll><GitHubRepositoryList /></Scroll>} />
              <Route path="/github/:namespace/:name" element={<Scroll><GitHubRepositoryDetail /></Scroll>} />
              <Route path="/cron" element={<Scroll><CronList /></Scroll>} />
              <Route path="/cron/:namespace/:name" element={<Scroll><CronDetail /></Scroll>} />
              <Route path="/slack" element={<Scroll><SlackAgentsPage /></Scroll>} />
              <Route path="/slack/:namespace/:name" element={<Scroll><SlackAgentDetail /></Scroll>} />
              <Route path="/settings" element={<Scroll><SettingsLayout /></Scroll>}>
                <Route index element={<SettingsScreen />} />
                <Route path="connection" element={<SettingsConnectionPage />} />
                <Route path="credentials" element={<SettingsCredentialsPage />} />
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
