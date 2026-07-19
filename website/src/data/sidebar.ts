export const repositoryUrl = 'https://github.com/gratefulagents/gratefulagents';

export interface SidebarSection {
  label: string;
  /** Manifest-style key shown as the section eyebrow in navigation. */
  kind: string;
  items: string[];
}

/** Mirrors user-docs/sidebars.ts. Ids are doc file paths without extension. */
export const sections: SidebarSection[] = [
  {label: 'Overview', kind: 'Overview', items: ['intro']},
  {
    label: 'Getting started',
    kind: 'GetStarted',
    items: [
      'getting-started/quick-start',
      'getting-started/self-hosting-kind',
      'getting-started/self-hosting-k3s',
      'getting-started/cloudflare-access',
      'getting-started/web-desktop-workspaces',
      'getting-started/sign-in',
      'getting-started/navigation',
    ],
  },
  {
    label: 'Agent runs',
    kind: 'Run',
    items: [
      'runs/start-a-run',
      'runs/chat-with-agent',
      'runs/plan-autopilot-stop',
      'runs/review-activity',
      'runs/diffs-and-pull-requests',
      'runs/meta-harness-traces',
    ],
  },
  {
    label: 'Projects and schedules',
    kind: 'Project',
    items: ['projects/projects', 'projects/run-defaults', 'projects/cron'],
  },
  {
    label: 'Integrations',
    kind: 'Integration',
    items: [
      'integrations/connection-secrets',
      'integrations/github',
      'integrations/linear',
      'integrations/slack',
    ],
  },
  {
    label: 'Resources',
    kind: 'Resource',
    items: ['settings/resources', 'settings/skill-packages'],
  },
  {
    label: 'Settings',
    kind: 'Settings',
    items: [
      'settings/account-appearance',
      'settings/connection',
      'settings/desktop-updates',
      'settings/credentials',
      'settings/soul',
      'settings/role-models',
      'settings/git-identity',
    ],
  },
  {
    label: 'Collaboration',
    kind: 'Share',
    items: ['collaboration/sharing-and-permissions', 'collaboration/shared-with-me'],
  },
  {label: 'Administration', kind: 'Admin', items: ['settings/users']},
  {
    label: 'Results and history',
    kind: 'Result',
    items: [
      'results/run-history-usage',
      'results/observability',
      'results/pull-request-feedback',
    ],
  },
  {
    label: 'Troubleshooting',
    kind: 'Triage',
    items: ['troubleshooting/common-issues', 'troubleshooting/credentials-integrations'],
  },
];

export const orderedIds = sections.flatMap((s) => s.items);

export function docPath(id: string): string {
  return id === 'intro' ? '/docs/' : `/docs/${id}/`;
}

export function sectionOf(id: string): SidebarSection | undefined {
  return sections.find((s) => s.items.includes(id));
}
