import assert from 'node:assert/strict';
import test from 'node:test';

import { generateAltStoreSource } from './generate-altstore-source.mjs';

function metadataName(tag) {
  return `gratefulagents-${tag}-ios-arm64-altstore-metadata.json`;
}

function metadata(tag, overrides = {}) {
  return {
    schemaVersion: 1,
    bundleIdentifier: 'dev.gratefulagents.app',
    version: tag.startsWith('v') ? tag.slice(1) : tag,
    buildVersion: overrides.buildVersion ?? (tag.startsWith('v') ? tag.slice(1) : tag),
    minOSVersion: '15.0',
    entitlements: [],
    privacy: {},
    ...overrides,
  };
}

function release(tag, options = {}) {
  const {
    ipa = true,
    metadataAsset = true,
    createdAt = '2026-01-01T12:00:00Z',
    draft = false,
    prerelease = false,
    size = 42_000_000,
    url = `https://github.com/gratefulagents/gratefulagents/releases/download/${tag}/gratefulagents-${tag}-ios-arm64-unsigned.ipa`,
  } = options;

  const assets = [];
  if (ipa) {
    assets.push({
      name: `gratefulagents-${tag}-ios-arm64-unsigned.ipa`,
      size,
      browser_download_url: url,
    });
  }
  if (metadataAsset) {
    assets.push({
      name: metadataName(tag),
      size: 256,
      browser_download_url: `https://github.com/example/${metadataName(tag)}`,
    });
  }

  return {
    tag_name: tag,
    name: tag,
    draft,
    prerelease,
    created_at: createdAt,
    published_at: draft ? null : createdAt,
    assets,
  };
}

function metadataMap(...tags) {
  return Object.fromEntries(tags.map((tag) => [metadataName(tag), metadata(tag)]));
}

test('builds an ordered AltStore Classic source from paginated GitHub releases', () => {
  const input = [
    [
      release('v0.4.0', { createdAt: '2026-04-04T12:00:00Z', draft: true, size: 44 }),
      release('v0.3.0', { createdAt: '2026-03-03T12:00:00Z', size: 33 }),
      release('v0.3.0-rc.1', { createdAt: '2026-03-01T12:00:00Z', prerelease: true }),
    ],
    [
      release('v0.2.0', { createdAt: '2026-02-02T12:00:00Z', size: 22 }),
      release('v0.1.0', { createdAt: '2026-01-01T12:00:00Z', metadataAsset: false }),
      release('v9.0.0', { createdAt: '2026-05-01T12:00:00Z', draft: true }),
    ],
  ];

  const source = generateAltStoreSource(input, {
    currentTag: 'v0.4.0',
    metadataByAssetName: metadataMap('v0.4.0', 'v0.3.0', 'v0.3.0-rc.1', 'v0.2.0', 'v9.0.0'),
  });

  assert.equal(source.name, 'Grateful Agents');
  assert.equal(source.nsfw, false);
  assert.equal(source.iconURL, 'https://gratefulagents.dev/logo.png');
  assert.deepEqual(source.featuredApps, ['dev.gratefulagents.app']);
  assert.equal(source.apps.length, 1);

  const app = source.apps[0];
  assert.equal(app.bundleIdentifier, 'dev.gratefulagents.app');
  assert.equal(app.category, 'developer');
  assert.deepEqual(app.appPermissions, { entitlements: [], privacy: {} });
  assert.deepEqual(
    app.versions.map(({ version }) => version),
    ['0.4.0', '0.3.0', '0.2.0'],
  );
  assert.deepEqual(app.versions[0], {
    version: '0.4.0',
    buildVersion: '0.4.0',
    marketingVersion: '0.4.0',
    date: '2026-04-04T12:00:00.000Z',
    localizedDescription: 'Grateful Agents v0.4.0. See the linked GitHub release for full notes.',
    downloadURL:
      'https://github.com/gratefulagents/gratefulagents/releases/download/v0.4.0/gratefulagents-v0.4.0-ios-arm64-unsigned.ipa',
    size: 44,
    minOSVersion: '15.0',
  });
});

test('always places the current release first, then orders history by date', () => {
  const source = generateAltStoreSource(
    [
      release('v1.0.0', { createdAt: '2026-01-01T00:00:00Z' }),
      release('v1.2.0', { createdAt: '2025-12-01T00:00:00Z', draft: true }),
      release('v1.1.0', { createdAt: '2026-02-01T00:00:00Z' }),
    ],
    {
      currentTag: 'v1.2.0',
      metadataByAssetName: metadataMap('v1.0.0', 'v1.1.0', 'v1.2.0'),
    },
  );

  assert.deepEqual(
    source.apps[0].versions.map(({ version }) => version),
    ['1.2.0', '1.1.0', '1.0.0'],
  );
});

test('uses exact extracted build and permission metadata from the current IPA', () => {
  const name = metadataName('v1.0.0');
  const source = generateAltStoreSource([release('v1.0.0', { draft: true })], {
    currentTag: 'v1.0.0',
    metadataByAssetName: {
      [name]: metadata('v1.0.0', {
        buildVersion: '107',
        minOSVersion: '16.0',
        entitlements: ['com.apple.developer.associated-domains'],
        privacy: { NSCameraUsageDescription: 'Scan a workspace code.' },
      }),
    },
  });

  assert.equal(source.apps[0].versions[0].buildVersion, '107');
  assert.equal(source.apps[0].versions[0].minOSVersion, '16.0');
  assert.deepEqual(source.apps[0].appPermissions, {
    entitlements: ['com.apple.developer.associated-domains'],
    privacy: { NSCameraUsageDescription: 'Scan a workspace code.' },
  });
});

test('omits historical versions whose permissions differ from the app-level declaration', () => {
  const source = generateAltStoreSource(
    [release('v1.1.0', { draft: true }), release('v1.0.0')],
    {
      currentTag: 'v1.1.0',
      metadataByAssetName: {
        [metadataName('v1.1.0')]: metadata('v1.1.0'),
        [metadataName('v1.0.0')]: metadata('v1.0.0', {
          privacy: { NSCameraUsageDescription: 'Scan a workspace code.' },
        }),
      },
    },
  );

  assert.deepEqual(source.apps[0].versions.map(({ version }) => version), ['1.1.0']);
  assert.deepEqual(source.apps[0].appPermissions, { entitlements: [], privacy: {} });
});

test('omits legacy historical IPAs without extracted metadata', () => {
  const source = generateAltStoreSource(
    [release('v1.1.0', { draft: true }), release('v1.0.0', { metadataAsset: false })],
    {
      currentTag: 'v1.1.0',
      metadataByAssetName: metadataMap('v1.1.0'),
    },
  );

  assert.deepEqual(source.apps[0].versions.map(({ version }) => version), ['1.1.0']);
});

test('requires the current release to contain both IPA and metadata assets', () => {
  assert.throws(
    () =>
      generateAltStoreSource([release('v1.0.0', { metadataAsset: false, draft: true })], {
        currentTag: 'v1.0.0',
      }),
    /must contain exactly one IPA and one AltStore metadata asset/,
  );
});

test('rejects an insecure IPA download URL', () => {
  assert.throws(
    () =>
      generateAltStoreSource([release('v1.0.0', { draft: true, url: 'http://example.com/app.ipa' })], {
        currentTag: 'v1.0.0',
        metadataByAssetName: metadataMap('v1.0.0'),
      }),
    /IPA download URL must use HTTPS/,
  );
});

test('rejects metadata that does not match the built app identity or release tag', () => {
  assert.throws(
    () =>
      generateAltStoreSource([release('v1.0.0', { draft: true })], {
        currentTag: 'v1.0.0',
        metadataByAssetName: {
          [metadataName('v1.0.0')]: metadata('v1.0.0', { bundleIdentifier: 'dev.example.wrong' }),
        },
      }),
    /bundleIdentifier must be dev\.gratefulagents\.app/,
  );
});

test('rejects duplicate IPA assets', () => {
  const current = release('v1.0.0', { draft: true });
  current.assets.push({ ...current.assets[0] });
  assert.throws(
    () =>
      generateAltStoreSource([current], {
        currentTag: 'v1.0.0',
        metadataByAssetName: metadataMap('v1.0.0'),
      }),
    /duplicate unsigned iOS IPA assets/,
  );
});

test('rejects malformed GitHub release input', () => {
  assert.throws(
    () => generateAltStoreSource({ tag_name: 'v1.0.0' }, { currentTag: 'v1.0.0' }),
    /GitHub releases input must be an array/,
  );
});
