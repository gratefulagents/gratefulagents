import clsx from 'clsx';
import Link from '@docusaurus/Link';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

const cards = [
  {title: 'Start your first chat', to: '/docs/getting-started/quick-start', text: 'Save a provider credential and start a repo-free chat in a Personal workspace.'},
  {title: 'Navigate the app', to: '/docs/getting-started/navigation', text: 'Find Home, Agent Ops, Observability, Projects, Shared, Resources, and Settings.'},
  {title: 'Configure Resources', to: '/docs/settings/resources', text: 'Manage skills, MCP servers, runtime profiles, policies, guardrails, modes, and roles.'},
  {title: 'Collaborate safely', to: '/docs/collaboration/sharing-and-permissions', text: 'Share projects or runs with the right workspace permission.'},
];

export default function Home(): JSX.Element {
  return (
    <Layout title="User Guide" description="User documentation for the gratefulagents platform app">
      <header className={clsx('hero heroBanner')}>
        <div className="container">
          <Heading as="h1" className="hero__title">gratefulagents user guide</Heading>
          <p className="hero__subtitle">Learn how to start chats, configure personal settings and workspace resources, and collaborate in the platform app.</p>
          <div>
            <Link className="button button--secondary button--lg" to="/docs/getting-started/quick-start">Get started</Link>
            <Link className="button button--outline button--secondary button--lg" to="/docs/settings/resources">Explore Resources</Link>
          </div>
        </div>
      </header>
      <main className="container margin-vert--xl">
        <div className="cardGrid">
          {cards.map((card) => (
            <Link key={card.to} to={card.to} className="guideCard">
              <h3>{card.title}</h3>
              <p>{card.text}</p>
            </Link>
          ))}
        </div>
      </main>
    </Layout>
  );
}
