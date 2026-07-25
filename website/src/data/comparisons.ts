export interface ComparisonRow {
  dimension: string;
  competitor: string;
  gratefulagents: string;
}

export interface FaqItem {
  question: string;
  answer: string;
}

export interface Comparison {
  slug: string;
  name: string;
  href: string;
  tagline: string;
  /** 2–3 sentence summary used on the /alternatives/ hub page. */
  summary: string;
  /** Honest description of what the competitor does well. */
  whatItIsGoodAt: string;
  rows: ComparisonRow[];
  faqs: FaqItem[];
}

export const comparisons: Comparison[] = [
  {
    slug: 'github-copilot',
    name: 'GitHub Copilot coding agent',
    href: '/alternatives/github-copilot/',
    tagline: "GitHub's cloud-managed AI coding agent",
    summary:
      "GitHub Copilot's coding agent is a tightly integrated cloud service that works directly inside GitHub.com. " +
      "It requires no infrastructure setup on your part, but all execution happens on GitHub's servers and you work within the model and tool choices GitHub provides. " +
      'GratefulAgents is the option for teams that need the agent to run inside their own network with their own model keys.',
    whatItIsGoodAt:
      "Copilot has unmatched GitHub-native integration — issues, PRs, and code review are all first-class surfaces. " +
      "The onboarding friction is essentially zero if your repositories are already on GitHub.com: no servers to provision, no Helm charts to deploy, no credentials to rotate. " +
      'For individuals and small teams who want a capable agent now and have no data-residency constraints, it is the lower-friction option.',
    rows: [
      {
        dimension: 'Where the agent executes',
        competitor: "GitHub's cloud infrastructure",
        gratefulagents: 'Your own Kubernetes cluster',
      },
      {
        dimension: 'Who holds model credentials',
        competitor: 'GitHub / Microsoft',
        gratefulagents: 'You — stored as Kubernetes secrets in your cluster',
      },
      {
        dimension: 'Model choice',
        competitor: "GitHub's supported model catalogue",
        gratefulagents: 'Claude, OpenAI, OpenRouter, Grok, Copilot — bring your own keys',
      },
      {
        dimension: 'Source code during a run',
        competitor: "Leaves your network to GitHub servers",
        gratefulagents: 'Stays inside your cluster sandbox',
      },
      {
        dimension: 'Licence',
        competitor: 'Proprietary SaaS',
        gratefulagents: 'AGPL-3.0 open source',
      },
      {
        dimension: 'Deployment model',
        competitor: 'Fully managed cloud; no ops required',
        gratefulagents: 'Self-managed Kubernetes; Helm chart provided',
      },
      {
        dimension: 'Per-run observability',
        competitor: 'GitHub Actions logs and PR timeline',
        gratefulagents: 'Jaeger traces, cost, tokens, tool calls, subagent graphs',
      },
      {
        dimension: 'Trigger integrations',
        competitor: 'GitHub issues and PRs',
        gratefulagents: 'GitHub, Linear, Slack, cron',
      },
      {
        dimension: 'Client applications',
        competitor: 'github.com web UI',
        gratefulagents: 'Web, desktop (macOS/Linux), iOS, Android',
      },
      {
        dimension: 'Infrastructure overhead',
        competitor: 'None',
        gratefulagents: 'Requires a Kubernetes cluster (Kind for local eval, k3s for a server)',
      },
    ],
    faqs: [
      {
        question: 'Can GratefulAgents work with GitHub repositories?',
        answer:
          "Yes. GratefulAgents has a first-class GitHub integration: it can be triggered by issues, PR comments, and review feedback, and agents open and iterate on pull requests just as they would from any CI environment. The difference is that execution happens inside your cluster rather than on GitHub's servers.",
      },
      {
        question: 'Do I need to leave GitHub to use GratefulAgents?',
        answer:
          'No. GratefulAgents connects to your existing GitHub repositories via a GitHubRepository CRD. You keep GitHub as your code host and collaboration surface; GratefulAgents only adds a self-hosted execution layer for the agent runs themselves.',
      },
      {
        question: 'Is GratefulAgents a drop-in replacement for GitHub Copilot?',
        answer:
          "Not exactly. Copilot is deeply embedded in the GitHub.com product — inline suggestions, PR summaries, the coding agent — and GratefulAgents does not replicate the inline editor suggestions. GratefulAgents focuses on autonomous agentic runs: longer tasks, multi-step reasoning, pull-request authoring, and review-feedback iteration.",
      },
      {
        question: 'What are the infrastructure requirements to try GratefulAgents?',
        answer:
          "For a local evaluation you need Docker and the Kind CLI; the getting-started guide walks through a single-command cluster bootstrap. For a persistent self-hosted installation a fresh Debian or Ubuntu server is sufficient — the guide uses k3s. No managed Kubernetes service is required.",
      },
    ],
  },
  {
    slug: 'devin',
    name: 'Devin',
    href: '/alternatives/devin/',
    tagline: "Cognition's fully managed autonomous coding agent",
    summary:
      "Devin is a polished, cloud-hosted autonomous coding agent built by Cognition. " +
      "It is a managed product with a thoughtful UI and a focus on long-horizon engineering tasks. " +
      "GratefulAgents is the alternative for teams that need the same class of autonomous agent capability but require it to run entirely within their own infrastructure.",
    whatItIsGoodAt:
      "Devin is a focused, managed product with real engineering behind it. " +
      "Its onboarding is handled entirely by Cognition — there is no cluster to stand up, no Helm chart to configure, and no credential rotation to manage. " +
      "The product UI is thoughtfully designed for following long-horizon tasks, and Cognition continues to invest heavily in autonomous task completion quality. " +
      "For teams without Kubernetes expertise or without data-residency requirements, Devin's zero-ops model is a genuine advantage.",
    rows: [
      {
        dimension: 'Where the agent executes',
        competitor: "Cognition's cloud infrastructure",
        gratefulagents: 'Your own Kubernetes cluster',
      },
      {
        dimension: 'Who holds model credentials',
        competitor: 'Cognition',
        gratefulagents: 'You — stored as Kubernetes secrets in your cluster',
      },
      {
        dimension: 'Model choice',
        competitor: "Cognition's internal models",
        gratefulagents: 'Claude, OpenAI, OpenRouter, Grok, Copilot — bring your own keys',
      },
      {
        dimension: 'Source code during a run',
        competitor: "Leaves your network to Cognition servers",
        gratefulagents: 'Stays inside your cluster sandbox',
      },
      {
        dimension: 'Licence',
        competitor: 'Proprietary SaaS',
        gratefulagents: 'AGPL-3.0 open source',
      },
      {
        dimension: 'Deployment model',
        competitor: 'Fully managed cloud; no ops required',
        gratefulagents: 'Self-managed Kubernetes; Helm chart provided',
      },
      {
        dimension: 'Per-run observability',
        competitor: "Devin's own session UI",
        gratefulagents: 'Jaeger traces, cost, tokens, tool calls, subagent graphs',
      },
      {
        dimension: 'Trigger integrations',
        competitor: 'Web UI; GitHub integration',
        gratefulagents: 'GitHub, Linear, Slack, cron',
      },
      {
        dimension: 'Client applications',
        competitor: 'Web UI',
        gratefulagents: 'Web, desktop (macOS/Linux), iOS, Android',
      },
      {
        dimension: 'Infrastructure overhead',
        competitor: 'None',
        gratefulagents: 'Requires a Kubernetes cluster (Kind for local eval, k3s for a server)',
      },
    ],
    faqs: [
      {
        question: 'How does GratefulAgents compare to Devin for autonomous tasks?',
        answer:
          "Both products are designed for autonomous, long-horizon coding tasks. The architectural difference is execution location: Devin runs on Cognition's servers, while GratefulAgents runs in isolated sandboxes inside your own Kubernetes cluster. GratefulAgents is in early development, so Devin's level of task-completion polish may be higher right now — but GratefulAgents gives you full control of the execution environment and the model contract.",
      },
      {
        question: 'Can I inspect what the agent did in GratefulAgents?',
        answer:
          "Yes. Every run produces a full Jaeger trace showing each step, tool call, model prompt, cost, token count, and subagent dependency graph. You can replay the trace, compare runs across models, and audit compactions and errors. This trace lives entirely within your cluster.",
      },
      {
        question: 'Does GratefulAgents support the same trigger workflows as Devin?',
        answer:
          "GratefulAgents supports GitHub issue and PR triggers, Linear triggers, Slack triggers, and cron schedules. Agents can open pull requests, read review feedback, and iterate — covering the main agentic workflow loop.",
      },
      {
        question: 'Is GratefulAgents production-ready?',
        answer:
          "GratefulAgents is in early development and bugs are expected. There is no documented high-availability configuration, managed hosting, or production support SLA at this time. Teams evaluating it for critical workloads should treat it as early-adopter software and test it thoroughly in a non-production environment first.",
      },
    ],
  },
  {
    slug: 'openhands',
    name: 'OpenHands',
    href: '/alternatives/openhands/',
    tagline: 'Open-source self-hosted agent harness by All Hands AI',
    summary:
      "OpenHands (formerly OpenDevin) is a genuinely open-source, self-hosted coding agent framework with a strong research and benchmark track record and a large community. " +
      "Both OpenHands and GratefulAgents are open-source and self-hosted — the meaningful distinction is product scope: " +
      "GratefulAgents ships a Kubernetes-native control plane, multi-platform client apps, first-class trigger integrations, and an Agent Ops observability console around the harness itself.",
    whatItIsGoodAt:
      "OpenHands has a large and active open-source community, a strong academic research lineage, and an impressive set of agent benchmark results. " +
      "Its architecture is container-first and straightforward to run locally via Docker Compose, making it accessible without Kubernetes expertise. " +
      "The plugin and tool system is extensible, and the project moves quickly. " +
      "If you want a well-established open-source harness with a rich community and research backing, OpenHands is a serious option that should be evaluated on its own merits.",
    rows: [
      {
        dimension: 'Where the agent executes',
        competitor: 'Docker container on any host you provide',
        gratefulagents: 'Kubernetes-native isolated sandbox (kubernetes-sigs/agent-sandbox)',
      },
      {
        dimension: 'Licence',
        competitor: 'MIT (open source)',
        gratefulagents: 'AGPL-3.0 (open source)',
      },
      {
        dimension: 'Model choice',
        competitor: 'Any LiteLLM-compatible model',
        gratefulagents: 'Claude, OpenAI, OpenRouter, Grok, Copilot — bring your own keys',
      },
      {
        dimension: 'Kubernetes-native control plane',
        competitor: 'Not built in; you operate the container directly',
        gratefulagents: 'CRDs: AgentRun, Project, GitHubRepository, LinearProject, SlackAgent, Cron',
      },
      {
        dimension: 'Client applications',
        competitor: 'Web UI',
        gratefulagents: 'Web, desktop (macOS/Linux), iOS, Android',
      },
      {
        dimension: 'Trigger integrations',
        competitor: 'GitHub; community plugins',
        gratefulagents: 'GitHub, Linear, Slack, cron — first-class CRD-backed',
      },
      {
        dimension: 'Per-run observability',
        competitor: 'Session logs and replay',
        gratefulagents: 'Jaeger traces, cost, tokens, tool calls, subagent dependency graphs',
      },
      {
        dimension: 'Sharing and permissions',
        competitor: 'Not built in',
        gratefulagents: 'First-class sharing and permissions model',
      },
      {
        dimension: 'Deployment complexity',
        competitor: 'Docker Compose; lower barrier to entry',
        gratefulagents: 'Kubernetes required; higher initial ops investment',
      },
      {
        dimension: 'Community and research track record',
        competitor: 'Large community; strong academic benchmark results',
        gratefulagents: 'Early development; smaller community',
      },
    ],
    faqs: [
      {
        question: 'If both are open source, why choose one over the other?',
        answer:
          "The choice comes down to what you need around the core harness. OpenHands is a well-established, Docker-first framework with a large community and strong research backing — an excellent pick if you want a proven open-source harness you can operate with minimal infrastructure. GratefulAgents is the right fit if you need a Kubernetes-native control plane with CRD-backed trigger integrations, multi-platform client apps (web, desktop, iOS, Android), a built-in Agent Ops observability console, and first-class sharing and permissions — delivered as a cohesive product rather than a framework.",
      },
      {
        question: 'Does GratefulAgents require Kubernetes, and is that a disadvantage?',
        answer:
          "Yes, GratefulAgents requires Kubernetes. For a local evaluation, Kind (Kubernetes in Docker) is the recommended path and takes only a few minutes to set up. For a production-style installation, k3s on a fresh Debian or Ubuntu server is the supported path. If you have no Kubernetes experience and do not intend to acquire it, OpenHands' Docker Compose path is genuinely lower friction.",
      },
      {
        question: "How does GratefulAgents' observability differ from OpenHands?",
        answer:
          "GratefulAgents emits a full Jaeger-compatible distributed trace for every run, capturing each step, tool call, model prompt, cost, token count, compaction event, and subagent dependency graph. The Agent Ops console lets you compare runs across models and review errors. OpenHands provides session logs and replay within its own UI. The GratefulAgents approach is designed for teams that need audit-grade traceability across many concurrent runs.",
      },
      {
        question: 'Can I migrate from OpenHands to GratefulAgents?',
        answer:
          "There is no automated migration path. Both products use different architectures, CRD models, and credential storage conventions. The practical migration is to run GratefulAgents alongside your existing OpenHands setup, validate it on a representative workload, and cut over trigger integrations once you are satisfied. Because GratefulAgents is in early development, a parallel-run approach is strongly recommended.",
      },
    ],
  },
];

export function getComparison(slug: string): Comparison | undefined {
  return comparisons.find((c) => c.slug === slug);
}
