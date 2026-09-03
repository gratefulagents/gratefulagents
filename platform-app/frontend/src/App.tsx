import * as React from "react";
import { BrowserRouter, Routes, Route, Link, Navigate, useLocation, useNavigate } from "react-router-dom";
import { MotionConfig } from "framer-motion";
import { cn } from "@/lib/utils";

import { ProjectTree } from "@/components/shell/ProjectTree";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
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
import { RedirectPreservingQuery, SectionNotFound } from "@/components/shell/routing";
import { CommandPalette, type PaletteItem } from "@/components/shell/CommandPalette";
import { useGlobalShortcuts, useViewport } from "@/components/shell/shortcuts";
import { ShortcutsOverlay } from "@/components/shell/ShortcutsOverlay";
import { OfflineBanner } from "@/components/shell/OfflineBanner";
import { WorkspaceSwitcher } from "@/components/shell/WorkspaceSwitcher";
import { Toaster } from "@/components/ui/toaster";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useProjects } from "@/hooks/useWatchedList";
import { useRecentsTracker } from "@/hooks/useRecents";
import { useRunTabsTracker, runTabsScope } from "@/hooks/useRunTabs";
import { RunTabs } from "@/components/shell/RunTabs";
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
  Bug,
  Users,
  PanelLeft,
  Plus,
  Settings as SettingsIcon,
  Shield,
  ShieldCheck,
} from "lucide-react";
import { isTauri, platform } from "@/lib/platform";
import { APP_VERSION } from "@/lib/build-info";

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

/** Uppercase micro-label used for every sidebar section header. */
function SidebarSectionLabel({
  children,
  action,
}: {
  children: React.ReactNode;
  action?: React.ReactNode;
}) {
  return (
    <SidebarGroupLabel
      className={cn(
        "group/label h-7 px-2 pr-1 mb-0.5",
        "text-[10.5px] uppercase tracking-[0.1em] font-semibold text-muted-foreground/70",
        "flex items-center justify-between select-none",
      )}
    >
      <span>{children}</span>
      {action}
    </SidebarGroupLabel>
  );
}

/** A single top-level navigation row: icon, label, optional trailing slot. */
function SidebarNavItem({
  to,
  active,
  label,
  icon: Icon,
  trailing,
}: {
  to: string;
  active: boolean;
  label: string;
  icon: React.ComponentType<{ className?: string; strokeWidth?: number }>;
  trailing?: React.ReactNode;
}) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        render={<Link to={to} />}
        isActive={active}
        tooltip={label}
        className={cn(
          "group/nav relative h-[30px] rounded-[7px] px-2 gap-2.5 text-[12.5px]",
          "text-sidebar-foreground/85 transition-colors duration-[var(--dur-fast)]",
          "hover:bg-sidebar-accent hover:text-foreground",
          "data-active:bg-[color:var(--color-primary)]/10 data-active:text-foreground",
          "data-active:font-medium",
          // Active indicator: a short pill hugging the left edge.
          "before:absolute before:left-0 before:top-1/2 before:h-3.5 before:w-[2px] before:-translate-y-1/2",
          "before:rounded-full before:bg-[color:var(--color-primary)] before:opacity-0 before:transition-opacity",
          "data-active:before:opacity-100",
          "group-data-[collapsible=icon]:before:hidden",
        )}
      >
        <Icon
          strokeWidth={1.75}
          className={cn(
            "size-[15px] shrink-0 transition-colors duration-[var(--dur-fast)]",
            active
              ? "text-[color:var(--color-primary)]"
              : "text-muted-foreground group-hover/nav:text-foreground/80",
          )}
        />
        <span className="truncate tracking-tight">{label}</span>
        {trailing}
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

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
  const needsAttention = runs.some((run) => getRunAttention(run).kind !== "none");
  const isSettings = location.pathname.startsWith("/settings");

  return (
    <Sidebar
      collapsible="icon"
      className={cn(
        "bg-[color:var(--color-sidebar)] backdrop-blur-[22px] saturate-150",
        "border-r border-sidebar-border",
      )}
    >
      {/* Space for macOS traffic lights — stays clean on iPad too */}
      <SidebarHeader className="min-h-[44px] pt-safe px-2 pb-0 flex items-center gap-2 drag-region">
        <div className="hidden md:block pl-[66px]" aria-hidden />
      </SidebarHeader>

      <SidebarContent className="sidebar-scroll gap-0 px-1.5 no-drag">
        <SidebarGroup className="pt-0 pb-1">
          <SidebarGroupContent>
            {isTauri ? (
              <WorkspaceSwitcher />
            ) : (
              <div className="flex items-center gap-2.5 px-2 py-1 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0">
                <img src="/logo.png" alt="" className="size-[22px] shrink-0 rounded-[6px]" />
                <span className="truncate text-[12.5px] font-semibold tracking-tight group-data-[collapsible=icon]:hidden">
                  Grateful Agents
                </span>
              </div>
            )}
            <div
              className={cn(
                "mt-1.5 flex items-center gap-1.5 px-2 font-mono text-[10px] tracking-tight text-muted-foreground/60",
                "group-data-[collapsible=icon]:hidden",
              )}
              title={`App version ${APP_VERSION}`}
            >
              <span className="size-1 rounded-full bg-[color:var(--tone-success)]/70" aria-hidden />
              <span className="truncate">build v{APP_VERSION}</span>
            </div>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup className="py-1">
          <SidebarGroupContent>
            <SidebarMenu className="gap-px">
              <SidebarNavItem to="/" label="Home" icon={HomeIcon} active={location.pathname === "/"} />
              <SidebarNavItem
                to="/runs"
                label="Agent Ops"
                icon={Radio}
                active={location.pathname === "/runs"}
                trailing={
                  needsAttention ? (
                    <span
                      className="relative ml-auto mr-0.5 inline-flex size-1.5 shrink-0 rounded-full bg-[color:var(--tone-warning)] group-data-[collapsible=icon]:hidden"
                      role="img"
                      aria-label="Runs need attention"
                      title="Runs need attention"
                    >
                      <span className="absolute inset-0 rounded-full bg-[color:var(--tone-warning)] opacity-60 motion-safe:animate-ping" />
                    </span>
                  ) : undefined
                }
              />
              <SidebarNavItem
                to="/observability"
                label="Observability"
                icon={Activity}
                active={location.pathname === "/observability"}
              />
              <SidebarNavItem
                to="/bug-reports"
                label="Bug Reports"
                icon={Bug}
                active={location.pathname === "/bug-reports"}
              />
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup className="py-1 group/section">
          <SidebarSectionLabel
            action={
              <CreateProjectDialog
                trigger={
                  <button
                    type="button"
                    title="New project"
                    aria-label="New project"
                    className={cn(
                      "grid size-5 place-items-center rounded-[5px] text-muted-foreground/60",
                      "transition-[opacity,background-color,color] duration-[var(--dur-fast)]",
                      "opacity-75 group-hover/section:opacity-100 focus-visible:opacity-100",
                      "hover:bg-sidebar-accent hover:text-foreground",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
                    )}
                  >
                    <Plus className="size-3.5" strokeWidth={2} />
                  </button>
                }
              />
            }
          >
            Projects
          </SidebarSectionLabel>
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

        <SidebarGroup className="py-1">
          <SidebarSectionLabel>Workspace</SidebarSectionLabel>
          <SidebarGroupContent>
            <SidebarMenu className="gap-px">
              <SidebarNavItem to="/shared" label="Shared" icon={Users} active={location.pathname === "/shared"} />
              <SidebarNavItem
                to="/security"
                label="Security"
                icon={Shield}
                active={location.pathname.startsWith("/security")}
              />
              <SidebarNavItem
                to="/resources/skills"
                label="Resources"
                icon={Blocks}
                active={location.pathname.startsWith("/resources")}
              />
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {user?.role === "admin" && (
          <SidebarGroup className="py-1">
            <SidebarSectionLabel>Admin</SidebarSectionLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-px">
                <SidebarNavItem
                  to="/admin/users"
                  label="Users"
                  icon={ShieldCheck}
                  active={location.pathname === "/admin/users"}
                />
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>

      <SidebarFooter className="border-t border-sidebar-border px-1.5 pt-1.5 pb-[max(env(safe-area-inset-bottom),0.5rem)] no-drag">
        <Link
          to="/settings"
          className={cn(
            "group/user flex items-center gap-2.5 px-1.5 py-1.5 rounded-[8px]",
            "hover:bg-sidebar-accent transition-colors duration-[var(--dur-fast)]",
            "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
            "group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0",
            isSettings && "bg-[color:var(--color-primary)]/10",
          )}
          title="Settings"
          aria-current={isSettings ? "page" : undefined}
        >
          <div
            className={cn(
              "grid size-[26px] shrink-0 place-items-center overflow-hidden rounded-full",
              "bg-gradient-to-br from-[oklch(0.6_0.12_262)] to-[oklch(0.4_0.1_262)]",
              "text-[11px] font-semibold text-white/90",
              "ring-1 ring-inset ring-white/10 shadow-[0_0_0_1px_var(--color-sidebar-border)]",
            )}
          >
            {user?.picture ? (
              <img src={user.picture} alt="" className="size-full object-cover" />
            ) : (
              <span aria-hidden>{(user?.name || user?.username || "?").slice(0, 1).toUpperCase()}</span>
            )}
          </div>
          <div className="min-w-0 flex-1 group-data-[collapsible=icon]:hidden">
            <div className="truncate text-[12px] font-medium leading-4 tracking-tight">
              {user?.name || user?.username || "—"}
            </div>
            <div className="truncate font-mono text-[10.5px] leading-4 text-muted-foreground/70">
              {user?.email || "offline"}
            </div>
          </div>
          <SettingsIcon
            strokeWidth={1.75}
            className={cn(
              "size-[15px] shrink-0 transition-colors duration-[var(--dur-fast)]",
              "group-data-[collapsible=icon]:hidden",
              isSettings ? "text-[color:var(--color-primary)]" : "text-muted-foreground/70 group-hover/user:text-foreground",
            )}
          />
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
