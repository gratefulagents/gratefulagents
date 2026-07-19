import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  userGuideSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Getting started',
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
      type: 'category',
      label: 'Agent runs',
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
      type: 'category',
      label: 'Projects and schedules',
      items: [
        'projects/projects',
        'projects/run-defaults',
        'projects/cron',
      ],
    },
    {
      type: 'category',
      label: 'Integrations',
      items: [
        'integrations/connection-secrets',
        'integrations/github',
        'integrations/linear',
        'integrations/slack',
      ],
    },
    {
      type: 'category',
      label: 'Resources',
      items: [
        'settings/resources',
        'settings/skill-packages',
      ],
    },
    {
      type: 'category',
      label: 'Settings',
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
      type: 'category',
      label: 'Collaboration',
      items: [
        'collaboration/sharing-and-permissions',
        'collaboration/shared-with-me',
      ],
    },
    {
      type: 'category',
      label: 'Administration',
      items: ['settings/users'],
    },
    {
      type: 'category',
      label: 'Results and history',
      items: [
        'results/run-history-usage',
        'results/observability',
        'results/pull-request-feedback',
      ],
    },
    {
      type: 'category',
      label: 'Troubleshooting',
      items: [
        'troubleshooting/common-issues',
        'troubleshooting/credentials-integrations',
      ],
    },
  ],
};

export default sidebars;
