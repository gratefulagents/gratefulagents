/**
 * Positioning data for /differences/.
 *
 * Deliberately narrow: stable architectural facts only. No pricing, benchmark,
 * funding, or customer claims — those go stale and become inaccurate — and no
 * editorializing about other vendors' products.
 */

export interface DifferenceRow {
  dimension: string;
  laptop: string;
  devin: string;
  copilot: string;
  gratefulagents: string;
}

export const differenceRows: DifferenceRow[] = [
  {
    dimension: 'Where the agent runs',
    laptop: 'Your laptop, while it is awake',
    devin: "Cognition's cloud",
    copilot: "GitHub's cloud",
    gratefulagents: 'Your own Kubernetes cluster',
  },
  {
    dimension: 'Parallel runs',
    laptop: 'As many terminals as one machine can take',
    devin: "Set by the vendor's plan",
    copilot: "Set by GitHub's plan",
    gratefulagents: 'One pod per run, capped by limits you set',
  },
  {
    dimension: 'Keeps working when you log off',
    laptop: 'No',
    devin: 'Yes',
    copilot: 'Yes',
    gratefulagents: 'Yes',
  },
  {
    dimension: 'Where the repository is checked out',
    laptop: 'Your working copy',
    devin: "Cognition's infrastructure",
    copilot: "GitHub's infrastructure",
    gratefulagents: 'A sandbox pod inside your cluster',
  },
  {
    dimension: 'Who holds the model credentials',
    laptop: 'You, in local config files',
    devin: 'Cognition',
    copilot: 'GitHub / Microsoft',
    gratefulagents: 'You, as secrets in your cluster',
  },
  {
    dimension: 'Model choice',
    laptop: 'Whatever that CLI supports',
    devin: "Cognition's own models",
    copilot: "GitHub's supported catalog",
    gratefulagents: 'Claude, OpenAI, OpenRouter, Grok, GitHub Copilot, on your keys',
  },
  {
    dimension: 'License',
    laptop: 'Varies by tool',
    devin: 'Proprietary SaaS',
    copilot: 'Proprietary SaaS',
    gratefulagents: 'AGPL-3.0 open source',
  },
  {
    dimension: 'Who operates it',
    laptop: 'Each developer',
    devin: 'The vendor',
    copilot: 'The vendor',
    gratefulagents: 'You: a Helm chart on a k3s server, or Kind for a trial',
  },
  {
    dimension: 'Per-run observability',
    laptop: 'Terminal output',
    devin: "The vendor's session view",
    copilot: 'Agent session logs',
    gratefulagents: 'Traces, cost, tokens, tool calls, subagent graphs',
  },
  {
    dimension: 'Trigger surfaces',
    laptop: 'Someone typing in a terminal',
    devin: 'Web UI, Slack',
    copilot: 'GitHub issues and pull requests',
    gratefulagents: 'GitHub, Linear, Slack, Cron',
  },
  {
    dimension: 'Client applications',
    laptop: 'Terminal or editor',
    devin: 'Web',
    copilot: 'GitHub web',
    gratefulagents: 'Web, desktop (macOS, Linux), iOS, Android',
  },
];

export interface FaqItem {
  question: string;
  answer: string;
}

export const faqs: FaqItem[] = [
  {
    question: 'I already run a coding agent CLI on my laptop. Why add this?',
    answer:
      'Keep using it for work you want to watch closely. GratefulAgents is for the rest: tasks that should run in parallel, keep going after you log off, start from an issue, ticket, Slack message, or schedule, and leave a record your team can read. It can use the same model providers.',
  },
  {
    question: 'Is GratefulAgents a self-hosted alternative to Devin?',
    answer:
      'Yes. Both run autonomous coding tasks against your repositories. The difference is where that happens: Devin executes on Cognition\u2019s infrastructure, while GratefulAgents executes in sandbox pods inside a Kubernetes cluster you run, using model credentials you hold.',
  },
  {
    question: "Is GratefulAgents a self-hosted alternative to GitHub Copilot's coding agent?",
    answer:
      'Yes. Copilot\u2019s coding agent is a cloud feature of GitHub.com, so the checkout and the agent process live on GitHub\u2019s infrastructure. GratefulAgents runs the same class of work inside your own cluster, and can be triggered from Linear, Slack, or a cron schedule as well as from GitHub.',
  },
  {
    question: 'Does my source code leave my network?',
    answer:
      'The checkout, the sandbox, the tool calls, and the run history stay inside your cluster. Inference is the exception: prompts and the repository context included in them are sent to whichever model provider you configure, under that provider\u2019s terms. If you cannot send code to a third party at all, you need a self-hosted inference endpoint as well.',
  },
  {
    question: 'Which models can I use?',
    answer:
      'Any provider whose credentials you store in your workspace: Claude (Anthropic), OpenAI, OpenRouter, Grok (xAI), and GitHub Copilot. Model choice is configurable per project and per role, so you are not tied to one vendor\u2019s catalog.',
  },
  {
    question: 'What do I need to run it?',
    answer:
      'A fresh Debian or Ubuntu server, such as a cloud VM. The k3s guide turns it into a single-node cluster running GratefulAgents. To look around first, the Kind guide runs the same Helm chart on a macOS or Linux laptop.',
  },
];
