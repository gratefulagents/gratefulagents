import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const repositoryUrl = 'https://github.com/gratefulagents/gratefulagents';

const config: Config = {
  title: 'gratefulagents User Guide',
  tagline: 'How to use the gratefulagents platform app',
  favicon: 'img/logo.png',

  url: 'https://docs.gratefulagents.dev',
  baseUrl: '/',
  organizationName: 'gratefulagents',
  projectName: 'gratefulagents-user-docs',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: 'docs',
          routeBasePath: 'docs',
          sidebarPath: './sidebars.ts',
          editUrl: `${repositoryUrl}/edit/main/user-docs/`,
          showLastUpdateAuthor: false,
          showLastUpdateTime: true,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/logo.png',
    navbar: {
      title: 'gratefulagents',
      logo: {
        alt: 'gratefulagents logo',
        src: 'img/logo.png',
      },
      items: [
        {type: 'docSidebar', sidebarId: 'userGuideSidebar', position: 'left', label: 'User Guide'},
        {to: '/docs/getting-started/quick-start', label: 'Get started', position: 'left'},
        {href: repositoryUrl, label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Use the app',
          items: [
            {label: 'Quick start', to: '/docs/getting-started/quick-start'},
            {label: 'Resources', to: '/docs/settings/resources'},
            {label: 'Settings', to: '/docs/settings/account-appearance'},
          ],
        },
        {
          title: 'Open source',
          items: [
            {label: 'GitHub repository', href: repositoryUrl},
            {label: 'Documentation source', href: `${repositoryUrl}/tree/main/user-docs`},
            {label: 'Edit this page', href: `${repositoryUrl}/edit/main/user-docs/docs/intro.md`},
            {label: 'Report an issue', href: `${repositoryUrl}/issues/new`},
          ],
        },
        {
          title: 'Help',
          items: [
            {label: 'Troubleshooting', to: '/docs/troubleshooting/common-issues'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} gratefulagents.`,
    },
    prism: {
      theme: require('prism-react-renderer').themes.github,
      darkTheme: require('prism-react-renderer').themes.dracula,
      additionalLanguages: ['bash', 'json'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
