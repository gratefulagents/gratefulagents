/**
 * Positioning data for /differences/.
 *
 * Deliberately narrow: stable architectural facts only. No pricing, benchmark,
 * funding, or customer claims — those go stale and become inaccurate — and no
 * editorialising about other vendors' products.
 */

export interface DifferenceRow {
  dimension: string;
  devin: string;
  copilot: string;
  gratefulagents: string;
}

export const differenceRows: DifferenceRow[] = [
  {
    dimension: 'Where the agent runs',
    devin: "Cognition's cloud",
    copilot: "GitHub's cloud",
    gratefulagents: 'Your own Kubernetes cluster',
  },
  {
    dimension: 'Where the repository is checked out',
    devin: "Cognition's infrastructure",
    copilot: "GitHub's infrastructure",
    gratefulagents: 'A sandbox pod inside your cluster',
  },
  {
    dimension: 'Who holds the model credentials',
    devin: 'Cognition',
    copilot: 'GitHub / Microsoft',
    gratefulagents: 'You — stored in your own workspace',
  },
  {
    dimension: 'Model choice',
    devin: "Cognition's own models",
    copilot: "GitHub's supported catalogue",
    gratefulagents: 'Claude, OpenAI, OpenRouter, Grok, Copilot — your keys',
  },
  {
    dimension: 'Licence',
    devin: 'Proprietary SaaS',
    copilot: 'Proprietary SaaS',
    gratefulagents: 'AGPL-3.0 open source',
  },
  {
    dimension: 'Who operates it',
    devin: 'The vendor',
    copilot: 'The vendor',
    gratefulagents: 'You — Helm chart on Kind or k3s',
  },
  {
    dimension: 'Per-run observability',
    devin: "The vendor's session view",
    copilot: 'Agent session logs',
    gratefulagents: 'Traces, cost, tokens, tool calls, subagent graphs',
  },
  {
    dimension: 'Trigger surfaces',
    devin: 'Web UI, Slack',
    copilot: 'GitHub issues and pull requests',
    gratefulagents: 'GitHub, Linear, Slack, cron',
  },
  {
    dimension: 'Client applications',
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
      'Any provider whose credentials you store in your workspace: Claude (Anthropic), OpenAI, OpenRouter, Grok (xAI), and GitHub Copilot. Model choice is configurable per project and per role, so you are not tied to one vendor\u2019s catalogue.',
  },
  {
    question: 'What do I need to run it?',
    answer:
      'A Kubernetes cluster. The Kind guide stands one up on a macOS or Linux laptop for evaluation; the k3s guide covers a persistent install on a fresh Debian or Ubuntu server. Both are installed with the same Helm chart.',
  },
];
