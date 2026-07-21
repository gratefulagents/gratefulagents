#!/usr/bin/env node

import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const BUNDLE_IDENTIFIER = 'dev.gratefulagents.app';
const IPA_SUFFIX = '-ios-arm64-unsigned.ipa';
const METADATA_SUFFIX = '-ios-arm64-altstore-metadata.json';

function requireString(value, label) {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value;
}

function requireHttps(value, label) {
  const raw = requireString(value, label);
  let url;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(`${label} must be a valid URL`);
  }
  if (url.protocol !== 'https:') {
    throw new Error(`${label} must use HTTPS`);
  }
  return raw;
}

function stableReleaseAssetUrl(value, tag, assetName, label) {
  const url = new URL(requireHttps(value, label));
  const path = url.pathname.match(/^(.*\/releases\/download\/)[^/]+\/[^/]+$/);
  if (!path) {
    throw new Error(`${label} must be a GitHub release asset URL`);
  }

  // Draft assets use a temporary `untagged-*` path which becomes invalid as
  // soon as GitHub publishes the release. Construct the permanent tag path
  // while retaining the repository origin from the API response.
  url.pathname = `${path[1]}${encodeURIComponent(tag)}/${encodeURIComponent(assetName)}`;
  url.search = '';
  url.hash = '';
  return url.href;
}

function releaseList(input) {
  if (!Array.isArray(input)) {
    throw new Error('GitHub releases input must be an array');
  }

  // `gh api --paginate --slurp` returns one array per response page.
  return input.flatMap((page) => (Array.isArray(page) ? page : [page]));
}

function versionForTag(tag) {
  const version = tag.startsWith('v') ? tag.slice(1) : tag;
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`release tag ${JSON.stringify(tag)} is not a supported semantic version`);
  }
  return version;
}

function matchingAsset(release, expectedName, label) {
  const matches = release.assets.filter((asset) => asset?.name === expectedName);
  if (matches.length > 1) {
    throw new Error(`release ${release.tag_name} has duplicate ${label} assets`);
  }
  return matches[0] ?? null;
}

function validateMetadata(raw, tag) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    throw new Error(`release ${tag} AltStore metadata must be an object`);
  }
  if (raw.schemaVersion !== 1) {
    throw new Error(`release ${tag} has unsupported AltStore metadata schemaVersion`);
  }

  const bundleIdentifier = requireString(raw.bundleIdentifier, `release ${tag} bundleIdentifier`);
  if (bundleIdentifier !== BUNDLE_IDENTIFIER) {
    throw new Error(`release ${tag} bundleIdentifier must be ${BUNDLE_IDENTIFIER}`);
  }

  const version = requireString(raw.version, `release ${tag} version`);
  if (version !== versionForTag(tag)) {
    throw new Error(`release ${tag} version ${version} does not match its tag`);
  }
  const buildVersion = requireString(raw.buildVersion, `release ${tag} buildVersion`);
  const minOSVersion = requireString(raw.minOSVersion, `release ${tag} minOSVersion`);

  if (!Array.isArray(raw.entitlements) || raw.entitlements.some((item) => typeof item !== 'string')) {
    throw new Error(`release ${tag} entitlements must be an array of strings`);
  }
  if (!raw.privacy || typeof raw.privacy !== 'object' || Array.isArray(raw.privacy)) {
    throw new Error(`release ${tag} privacy must be an object`);
  }
  for (const [key, value] of Object.entries(raw.privacy)) {
    if (!/^NS[A-Za-z0-9]+UsageDescription$/.test(key) || typeof value !== 'string' || value.trim() === '') {
      throw new Error(`release ${tag} has invalid privacy permission ${key}`);
    }
  }

  return {
    version,
    buildVersion,
    minOSVersion,
    entitlements: [...new Set(raw.entitlements)].sort(),
    privacy: Object.fromEntries(Object.entries(raw.privacy).sort(([left], [right]) => left.localeCompare(right))),
  };
}

function versionFromRelease(release, metadataByAssetName, currentTag) {
  if (!release || typeof release !== 'object') {
    throw new Error('each GitHub release must be an object');
  }

  const tag = requireString(release.tag_name, 'release tag_name');
  if (!Array.isArray(release.assets)) {
    throw new Error(`release ${tag} assets must be an array`);
  }

  const ipaName = `gratefulagents-${tag}${IPA_SUFFIX}`;
  const metadataName = `gratefulagents-${tag}${METADATA_SUFFIX}`;
  const ipa = matchingAsset(release, ipaName, 'unsigned iOS IPA');
  const metadataAsset = matchingAsset(release, metadataName, 'AltStore metadata');

  if (!ipa && !metadataAsset) return null;
  if (!ipa || !metadataAsset) {
    // Releases created before AltStore support have an IPA but no extracted
    // metadata. Omit those rather than guessing values that AltStore requires
    // to match the bundle exactly. The current release must always be exact.
    if (tag !== currentTag && ipa && !metadataAsset) return null;
    throw new Error(`release ${tag} must contain exactly one IPA and one AltStore metadata asset`);
  }

  if (!Number.isSafeInteger(ipa.size) || ipa.size <= 0) {
    throw new Error(`release ${tag} IPA size must be a positive integer`);
  }

  const rawDate = release.published_at ?? release.created_at;
  const timestamp = Date.parse(requireString(rawDate, `release ${tag} date`));
  if (!Number.isFinite(timestamp)) {
    throw new Error(`release ${tag} date is not valid ISO 8601`);
  }

  const rawMetadata = metadataByAssetName[metadataName];
  if (rawMetadata === undefined) {
    throw new Error(`release ${tag} extracted AltStore metadata was not provided to the generator`);
  }
  const metadata = validateMetadata(rawMetadata, tag);

  return {
    tag,
    timestamp,
    permissions: {
      entitlements: metadata.entitlements,
      privacy: metadata.privacy,
    },
    entry: {
      version: metadata.version,
      buildVersion: metadata.buildVersion,
      marketingVersion: metadata.version,
      date: new Date(timestamp).toISOString(),
      localizedDescription: `Grateful Agents ${tag}. See the linked GitHub release for full notes.`,
      downloadURL: stableReleaseAssetUrl(
        ipa.browser_download_url,
        tag,
        ipaName,
        `release ${tag} IPA download URL`,
      ),
      size: ipa.size,
      minOSVersion: metadata.minOSVersion,
    },
  };
}

export function generateAltStoreSource(input, { currentTag, metadataByAssetName = {} } = {}) {
  currentTag = requireString(currentTag, 'currentTag');
  if (!metadataByAssetName || typeof metadataByAssetName !== 'object' || Array.isArray(metadataByAssetName)) {
    throw new Error('metadataByAssetName must be an object');
  }

  const eligible = releaseList(input).filter((release) => {
    if (!release || typeof release !== 'object') return true;
    if (release.tag_name === currentTag) return true;
    return release.draft !== true && release.prerelease !== true;
  });

  const versions = [];
  let current = null;
  for (const release of eligible) {
    const parsed = versionFromRelease(release, metadataByAssetName, currentTag);
    if (!parsed) continue;
    if (parsed.tag === currentTag) current = parsed;
    versions.push(parsed);
  }

  if (!current) {
    throw new Error(`current release ${currentTag} does not contain a complete AltStore build`);
  }

  // AltStore Classic declares permissions once at the app level, not per
  // version. Older builds with different permissions cannot be represented
  // accurately in the same source, so omit them rather than publishing
  // metadata that does not match the downloadable IPA.
  const currentPermissions = JSON.stringify(current.permissions);
  const compatibleVersions = versions.filter(
    ({ permissions }) => JSON.stringify(permissions) === currentPermissions,
  );

  compatibleVersions.sort((left, right) => {
    if (left.tag === currentTag) return -1;
    if (right.tag === currentTag) return 1;
    return right.timestamp - left.timestamp || right.tag.localeCompare(left.tag);
  });

  const seenBuilds = new Set();
  for (const { entry } of compatibleVersions) {
    const build = `${entry.version}\u0000${entry.buildVersion}`;
    if (seenBuilds.has(build)) {
      throw new Error(`duplicate AltStore version/build ${entry.version} (${entry.buildVersion})`);
    }
    seenBuilds.add(build);
  }

  return {
    name: 'Grateful Agents',
    subtitle: 'Official open-source iOS releases',
    description: 'Install and update the free, open-source Grateful Agents client with AltStore Classic.',
    iconURL: 'https://gratefulagents.dev/logo.png',
    website: 'https://gratefulagents.dev/',
    tintColor: '#7188D7',
    nsfw: false,
    featuredApps: [BUNDLE_IDENTIFIER],
    apps: [
      {
        name: 'Grateful Agents',
        bundleIdentifier: BUNDLE_IDENTIFIER,
        developerName: 'Grateful Agents contributors',
        subtitle: 'Open-source AI engineering cockpit',
        localizedDescription: 'Connect to your Grateful Agents workspace and operate agent runs from iPhone or iPad.',
        iconURL: 'https://gratefulagents.dev/logo.png',
        tintColor: '#7188D7',
        category: 'developer',
        screenshots: [
          {
            imageURL: 'https://gratefulagents.dev/screens/tablet-1.webp',
            width: 2752,
            height: 2064,
          },
          {
            imageURL: 'https://gratefulagents.dev/screens/tablet-2.webp',
            width: 2752,
            height: 2064,
          },
        ],
        versions: compatibleVersions.map(({ entry }) => entry),
        appPermissions: current.permissions,
      },
    ],
    news: [],
  };
}

function parseArguments(args) {
  const values = {};
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (!key?.startsWith('--') || value === undefined) {
      throw new Error(
        'usage: generate-altstore-source.mjs --releases <file> --metadata-dir <dir> --output <file> --current-tag <tag>',
      );
    }
    values[key.slice(2)] = value;
  }
  return values;
}

async function readMetadataDirectory(directory) {
  const metadata = {};
  for (const name of await readdir(directory)) {
    if (!name.endsWith(METADATA_SUFFIX)) continue;
    metadata[name] = JSON.parse(await readFile(path.join(directory, name), 'utf8'));
  }
  return metadata;
}

async function main() {
  const args = parseArguments(process.argv.slice(2));
  const releasesPath = requireString(args.releases, '--releases');
  const metadataDirectory = requireString(args['metadata-dir'], '--metadata-dir');
  const outputPath = requireString(args.output, '--output');
  const currentTag = requireString(args['current-tag'], '--current-tag');
  const releases = JSON.parse(await readFile(releasesPath, 'utf8'));
  const metadataByAssetName = await readMetadataDirectory(metadataDirectory);
  const source = generateAltStoreSource(releases, { currentTag, metadataByAssetName });

  await mkdir(path.dirname(outputPath), { recursive: true });
  await writeFile(outputPath, `${JSON.stringify(source, null, 2)}\n`);
}

const isMain = process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1]);
if (isMain) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
