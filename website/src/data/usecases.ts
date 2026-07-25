export interface UseCaseSection { heading: string; body: string[]; }
export interface UseCaseLink { label: string; href: string; }
export interface UseCase {
  slug: string;
  title: string;
  seoTitle: string;
  description: string;
  eyebrow: string;
  summary: string;
  problem: string;
  sections: UseCaseSection[];
  steps: string[];
  docs: UseCaseLink[];
}

export const useCases: UseCase[] = [
  {
    slug: 'self-hosted-coding-agents',
    title: 'Self-hosted AI coding agents for your Kubernetes cluster',
    seoTitle: 'Self-Hosted AI Coding Agents | GratefulAgents',
    description: 'Run self-hosted AI coding agents in your Kubernetes cluster. Keep agent execution in infrastructure you operate and inspect every run before review.',
    eyebrow: 'Self-hosted',
    summary: 'Run coding agents in your Kubernetes cluster with execution, configuration, and run history under your control.',
    problem: 'Hosted coding agents can move execution outside the infrastructure where you manage repositories and engineering tools. You need a way to run agent workloads in your cluster while retaining direct control over configuration and review.',
    sections: [
      {
        heading: 'Run the control plane in your cluster',
        body: [
          'gratefulagents is an open-source control plane that you deploy in your Kubernetes cluster. Its controller manager reconciles platform resources, serves the dashboard, and receives GitHub webhooks.',
          'Agent runs execute as isolated sandbox workloads built on kubernetes-sigs/agent-sandbox. You operate the cluster where those workloads run.',
        ],
      },
      {
        heading: 'Keep execution close to your repositories',
        body: [
          'Projects hold reusable defaults for each repository, including model, credential, runtime, and instructions. You can keep the run setup aligned with the repository it serves.',
          'The platform can connect GitHub, Linear, Slack, and Cron schedules to agent runs. You decide which integrations and projects are configured in your workspace.',
        ],
      },
      {
        heading: 'Choose models without changing the runtime',
        body: [
          'You configure your own credentials for Anthropic Claude, OpenAI, OpenRouter, xAI Grok, or GitHub Copilot. Credentials are configured per user in Settings.',
          'Model calls are made through the provider credentials you select. Review the data handling of each selected provider before using sensitive code.',
        ],
      },
      {
        heading: 'Inspect work before you merge it',
        body: [
          'Follow activity during a run, chat with the agent, and change between plan, autopilot, and stop behavior. When work completes, review its diffs and pull requests.',
          'Per-run views show cost, tokens, tool calls, subagents, compactions, errors, and OpenTelemetry traces for each turn.',
        ],
      },
    ],
    steps: [
      'Prepare a fresh Debian or Ubuntu server.',
      'Run make k3s-install with sudo.',
      'Configure model credentials in Settings.',
      'Create a project for your repository.',
      'Start and inspect an agent run.',
    ],
    docs: [
      {label: 'Self-host on k3s', href: '/docs/getting-started/self-hosting-k3s/'},
      {label: 'Configure credentials', href: '/docs/settings/credentials/'},
      {label: 'Create projects', href: '/docs/projects/projects/'},
      {label: 'Start a run', href: '/docs/runs/start-a-run/'},
    ],
  },
  {
    slug: 'github-issue-automation',
    title: 'Turn GitHub issues and Linear tickets into agent runs',
    seoTitle: 'GitHub Issue Automation for Agents | GratefulAgents',
    description: 'Turn GitHub issues, pull request review comments, and Linear tickets into agent runs in your cluster that can open reviewable pull requests.',
    eyebrow: 'Issue automation',
    summary: 'Connect GitHub and Linear work to agent runs that you can steer, inspect, and review as pull requests.',
    problem: 'Issue queues and pull request feedback create small, repeatable engineering tasks that still require context switching. You need a controlled path from the work item to an agent run and a reviewable pull request.',
    sections: [
      {
        heading: 'Connect work where it already arrives',
        body: [
          'GitHub and Linear are supported run triggers. Connect the integrations, then direct incoming work to the projects that hold defaults for the relevant repositories.',
          'Projects keep reusable model, credential, runtime, and instruction choices so you do not repeat the same run setup for each work item.',
        ],
      },
      {
        heading: 'Give the run repository context',
        body: [
          'Start the run with the issue, review comment, or ticket as its work context. You can chat with the agent while it works and steer it if the task needs clarification.',
          'Use plan, autopilot, and stop behavior to decide how the run should proceed. Specialist subagents can handle delegated work when a run uses them.',
        ],
      },
      {
        heading: 'Review the proposed change',
        body: [
          'Review activity as the run progresses, then inspect the resulting diff and pull request. The run record also exposes cost, tokens, tool calls, errors, and traces.',
          'Sharing and permissions let workspace teammates access the projects and runs that they need to review.',
        ],
      },
    ],
    steps: [
      'Connect your GitHub or Linear integration.',
      'Create a project for the repository.',
      'Set repository run defaults.',
      'Trigger or start a run from work.',
      'Review the diff and pull request.',
    ],
    docs: [
      {label: 'Connect GitHub', href: '/docs/integrations/github/'},
      {label: 'Connect Linear', href: '/docs/integrations/linear/'},
      {label: 'Set run defaults', href: '/docs/projects/run-defaults/'},
      {label: 'Review diffs and pull requests', href: '/docs/runs/diffs-and-pull-requests/'},
    ],
  },
  {
    slug: 'scheduled-agent-maintenance',
    title: 'Schedule recurring maintenance with coding agents',
    seoTitle: 'Scheduled Agent Maintenance | GratefulAgents',
    description: 'Schedule recurring agent runs for dependency updates, flaky test triage, documentation drift, and backlog grooming in your Kubernetes cluster.',
    eyebrow: 'Scheduled maintenance',
    summary: 'Use Cron-triggered agent runs to make recurring repository maintenance visible, reviewable, and repeatable.',
    problem: 'Dependency updates, flaky tests, documentation drift, and backlog cleanup often wait behind feature work. You need recurring work to arrive as an inspectable run rather than an untracked reminder.',
    sections: [
      {
        heading: 'Schedule maintenance work with Cron',
        body: [
          'Cron is a supported trigger for agent runs. Define recurring work for a project so maintenance can start on the schedule you configure.',
          'Use separate projects when repositories need different models, credentials, runtimes, or instructions for their maintenance tasks.',
        ],
      },
      {
        heading: 'Make recurring tasks specific',
        body: [
          'Set instructions that describe the maintenance task, such as reviewing dependency changes, investigating flaky tests, checking documentation drift, or grooming the backlog. The project stores reusable defaults for that repository.',
          'Resources such as skills, MCP servers, runtime profiles, MCP policies, guardrails, modes, and roles let you configure the workspace context used by runs.',
        ],
      },
      {
        heading: 'Review each scheduled outcome',
        body: [
          'A scheduled run remains a run you can inspect. Review its activity, chat with the agent when needed, and use plan, autopilot, or stop behavior to control work.',
          'Review diffs and pull requests before accepting a proposed change. Per-run observability helps you compare cost, tokens, tool calls, and errors across recurring work.',
        ],
      },
    ],
    steps: [
      'Create a project for the repository.',
      'Set maintenance instructions and run defaults.',
      'Configure a Cron schedule.',
      'Review each scheduled run.',
      'Inspect diffs before merging changes.',
    ],
    docs: [
      {label: 'Configure Cron schedules', href: '/docs/projects/cron/'},
      {label: 'Create projects', href: '/docs/projects/projects/'},
      {label: 'Set run defaults', href: '/docs/projects/run-defaults/'},
      {label: 'Review run activity', href: '/docs/runs/review-activity/'},
      {label: 'Review diffs and pull requests', href: '/docs/runs/diffs-and-pull-requests/'},
    ],
  },
  {
    slug: 'agent-observability',
    title: 'Audit AI coding agent cost, work, and traces',
    seoTitle: 'AI Agent Observability | GratefulAgents',
    description: 'Inspect agent cost, tokens, tool calls, errors, and OpenTelemetry traces across runs and models so coding agent work remains auditable during review.',
    eyebrow: 'Observability',
    summary: 'Inspect cost, tokens, tool calls, errors, and traces so you can audit agent work across runs and models.',
    problem: 'Agent work is difficult to operate when you can only see a final response or pull request. You need run-level evidence for what the agent did, what it cost, and where errors occurred.',
    sections: [
      {
        heading: 'Inspect a complete run record',
        body: [
          'Each run has activity you can review while work is in progress and after it completes. You can inspect diffs and pull requests alongside the work that produced them.',
          'A run can delegate to specialist subagents, and the run view shows a live dependency graph. This makes delegated work visible instead of hiding it behind a final result.',
        ],
      },
      {
        heading: 'Track usage and reliability signals',
        body: [
          'Per-run observability includes cost, tokens, tool calls, subagents, compactions, errors, and OpenTelemetry traces. You can review usage and run history across the workspace.',
          'Use these signals to compare agent behavior across configured models and to find runs that need investigation.',
        ],
      },
      {
        heading: 'Open traces for each turn',
        body: [
          'OpenTelemetry traces are available per turn. Use them to inspect the recorded work within a run when you need more detail than the activity feed provides.',
          'Trace visibility complements the run history, usage, diff, and pull request views. Together they give you an audit trail for agent work in your cluster.',
        ],
      },
    ],
    steps: [
      'Start a run with your chosen model.',
      'Review activity while the run works.',
      'Open per-turn traces when investigation is needed.',
      'Compare cost and token usage across runs.',
      'Review diffs and pull requests.',
    ],
    docs: [
      {label: 'Review observability', href: '/docs/results/observability/'},
      {label: 'Review run history and usage', href: '/docs/results/run-history-usage/'},
      {label: 'Inspect meta-harness traces', href: '/docs/runs/meta-harness-traces/'},
      {label: 'Review run activity', href: '/docs/runs/review-activity/'},
    ],
  },
];
