export interface FaqItem { q: string; a: string; }
export interface FaqGroup { id: string; label: string; items: FaqItem[]; }

export const faqGroups: FaqGroup[] = [
  {
    id: 'install',
    label: 'Installing and self-hosting',
    items: [
      {
        q: 'What do I need to self-host gratefulagents?',
        a: 'For the supported self-hosting path, you need a fresh single-node Debian or Ubuntu server and sudo access. The k3s installer deploys gratefulagents into that server cluster.',
      },
      {
        q: 'Can I try gratefulagents on my laptop?',
        a: 'Yes. The Kind installer runs a single-node Kubernetes cluster on a macOS or Linux laptop for local evaluation. Its dashboard binds to 127.0.0.1.',
      },
      {
        q: 'How do I install gratefulagents on Kubernetes?',
        a: 'Use make k3s-install on a fresh Debian or Ubuntu single-node server for the supported self-hosting path. For a local evaluation cluster, use make kind-install on macOS or Linux.',
      },
      {
        q: 'Does gratefulagents run in my Kubernetes cluster?',
        a: 'Yes. The controller manager runs in your cluster and reconciles the platform custom resources. Agent runs execute as isolated sandbox workloads in that cluster.',
      },
      {
        q: 'Can I expose the gratefulagents dashboard securely?',
        a: 'Yes. The self-hosting documentation covers using Cloudflare Access to expose the dashboard. The local Kind evaluation path keeps the dashboard bound to 127.0.0.1.',
      },
      {
        q: 'What infrastructure does the Helm chart deploy?',
        a: 'The Helm chart deploys the gratefulagents control plane. It can also deploy PostgreSQL with pgvector, MinIO, and Jaeger.',
      },
    ],
  },
  {
    id: 'models',
    label: 'Models and credentials',
    items: [
      {
        q: 'Which model providers are supported?',
        a: 'You can configure credentials for Anthropic Claude, OpenAI, OpenRouter, xAI Grok, and GitHub Copilot. You choose the provider credentials per user in Settings.',
      },
      {
        q: 'Do I need to bring my own AI model credentials?',
        a: 'Yes. gratefulagents uses credentials that you configure per user rather than providing a bundled model account. You can select models and role models after adding credentials.',
      },
      {
        q: 'Can different teammates use different model credentials?',
        a: 'Yes. Credentials are configured per user in Settings. This lets each user choose from the providers and credentials available to them.',
      },
      {
        q: 'Can I choose a different model for each repository?',
        a: 'Yes. Projects store reusable run defaults for a repository, including the model and credential. You can set those defaults alongside runtime and instruction choices.',
      },
      {
        q: 'Can I use GitHub Copilot with gratefulagents?',
        a: 'Yes. GitHub Copilot is one of the supported credential providers. Configure the credential in Settings before selecting it for a run or project default.',
      },
      {
        q: 'Where are model credentials configured?',
        a: 'Configure model provider credentials in Settings for each user. The credential and role model documentation explains the available configuration paths.',
      },
    ],
  },
  {
    id: 'control',
    label: 'Control, isolation, and data',
    items: [
      {
        q: 'Can I run AI coding agents without sending my code to a vendor?',
        a: 'gratefulagents runs its control plane and agent workloads in your Kubernetes cluster. Model calls use the provider credentials you configure, so review what your selected model provider receives before using sensitive code.',
      },
      {
        q: 'How is this different from a hosted coding agent?',
        a: 'With gratefulagents, you deploy the control plane and run agent workloads in infrastructure you operate. You also supply your own model provider credentials instead of using a bundled hosted model account.',
      },
      {
        q: 'How are agent runs isolated?',
        a: 'Each agent run executes in an isolated sandbox workload built on kubernetes-sigs/agent-sandbox. The controller manager reconciles runs and related custom resources in your cluster.',
      },
      {
        q: 'Can I control which MCP servers and skills agents use?',
        a: 'Yes. Reusable resources include skills, MCP servers, runtime profiles, MCP policies, guardrails, modes, and roles. You can configure these resources for your workspace and run setup.',
      },
      {
        q: 'Can I share agent projects with my team?',
        a: 'Yes. You can share projects and runs with workspace teammates and assign permissions. Projects also retain reusable defaults for each repository.',
      },
      {
        q: 'Can I audit what an agent did during a run?',
        a: 'Yes. You can inspect activity, diffs, and pull requests from a run. Per-run observability includes cost, tokens, tool calls, subagents, compactions, errors, and OpenTelemetry traces per turn.',
      },
    ],
  },
  {
    id: 'product',
    label: 'Runs, triggers, and clients',
    items: [
      {
        q: 'What can trigger an AI coding agent run?',
        a: 'You can trigger runs from GitHub, Linear, Slack, and Cron schedules. You can also start a run directly and chat with the agent while it works.',
      },
      {
        q: 'Can I steer an agent while it is working?',
        a: 'Yes. You can chat with an active agent and steer it mid-flight. You can switch its behavior between plan, autopilot, and stop modes.',
      },
      {
        q: 'Can agents create and review pull requests?',
        a: 'Agent runs can open pull requests, and you can review a run diff and pull request from the run workflow. The product documentation covers reviewing diffs and pull requests after agent work.',
      },
      {
        q: 'Can an agent delegate work to subagents?',
        a: 'Yes. A run can delegate work to specialist subagents. The run view shows a live dependency graph so you can follow the delegated work.',
      },
      {
        q: 'Which clients can I use to follow agent runs?',
        a: 'You can use the web client, desktop clients for Apple Silicon macOS and AMD64 or ARM64 Linux, plus iOS and Android clients. Compute continues to run in your cluster.',
      },
      {
        q: 'Is gratefulagents production ready?',
        a: 'No production-readiness claim is made. gratefulagents is in early development and expects bugs, and the documentation does not promise high availability, managed hosting, or a production support SLA.',
      },
    ],
  },
];

/** The subset surfaced on the homepage FAQ block. */
export const homepageFaq: FaqItem[] = [
  {
    q: 'Can I run AI coding agents without sending my code to a vendor?',
    a: 'gratefulagents runs its control plane and agent workloads in your Kubernetes cluster. Model calls use the provider credentials you configure, so review what your selected model provider receives before using sensitive code.',
  },
  {
    q: 'What do I need to self-host gratefulagents?',
    a: 'For the supported self-hosting path, you need a fresh single-node Debian or Ubuntu server and sudo access. The k3s installer deploys gratefulagents into that server cluster.',
  },
  {
    q: 'Which model providers are supported?',
    a: 'You can configure credentials for Anthropic Claude, OpenAI, OpenRouter, xAI Grok, and GitHub Copilot. You choose the provider credentials per user in Settings.',
  },
  {
    q: 'How is this different from a hosted coding agent?',
    a: 'With gratefulagents, you deploy the control plane and run agent workloads in infrastructure you operate. You also supply your own model provider credentials instead of using a bundled hosted model account.',
  },
  {
    q: 'What can trigger an AI coding agent run?',
    a: 'You can trigger runs from GitHub, Linear, Slack, and Cron schedules. You can also start a run directly and chat with the agent while it works.',
  },
  {
    q: 'Is gratefulagents production ready?',
    a: 'No production-readiness claim is made. gratefulagents is in early development and expects bugs, and the documentation does not promise high availability, managed hosting, or a production support SLA.',
  },
];
