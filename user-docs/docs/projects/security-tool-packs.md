# Deterministic security tool packs

The `internal/securitytoolpacks` package is the controller-owned execution contract for web/API, cryptography, network/protocol, and blockchain/smart-contract tools. It prevents an agent from choosing an executable or assembling a shell command. A scan selects a statically registered tool and provides only values accepted by that tool's typed argument declarations; the registry produces an argv vector and an immutable OCI digest for a sandbox executor.

This is an execution primitive, not evidence that a clean target is secure. Human approval workflows are intentionally not part of the contract. The configured target scope and budgets are authoritative.

## Registry and pins

`DefaultManifest` returns schema `security-tool-registry/v1` and declares the supported versions, accepted target/artifact types, fixed invocation, native media type, adapter, network/privilege requirements, limits, status mappings, retries, and redaction policy for:

- web/API: Playwright, OWASP ZAP, Schemathesis, RESTler, mitmproxy, SSLyze, Nuclei, and the authorization-matrix runner;
- cryptography: Wycheproof, RFC/NIST vectors, dudect, ctgrind, tlsfuzzer, differential tests, Tamarin, ProVerif, Verifpal, and OpenSSL inspection;
- network/protocol: Nmap, tshark, Zeek, Suricata, Scapy, boofuzz, testssl.sh, and Naabu;
- blockchain/smart-contract: fixed Foundry security tests, Echidna, Medusa (amd64), Slither, and Halmos.

The catalog distinguishes executable entries from catalog-only entries. Catalog-only tools remain visible with a reason but cannot produce an invocation. The `security-tools` image (`Dockerfile.security-tools`) carries checksum-locked Nuclei, Naabu, Foundry, and Echidna binaries on amd64 and arm64, plus Medusa on amd64. It also includes the built-in authorization-matrix and crypto-vector runners and complete digest-pinned OCI runtime closures for OWASP ZAP, Schemathesis, SSLyze, Nmap, Zeek, Suricata, Slither, and Halmos. Those closures execute as ordinary tools inside an unprivileged Bubblewrap root filesystem; they require neither Docker nor a container socket, and the agent cannot replace their executable or arguments. Nuclei uses the single reviewed template committed under `security-knowledge`; automatic template updates and caller-selected templates are disabled. Slither uses the immutable multi-architecture Trail of Bits toolbox index. Halmos uses a hash-locked Python/Forge/compiler closure with Z3 4.13.0.0, the first compatible Z3 release that publishes both Linux amd64 and arm64 wheels. Its replay record carries architecture-specific closure digests derived from the Python platform manifest, Forge binary, Slither compiler-root manifest, and both hashed Python lock files rather than mislabeling the Python base digest as the scanner closure.

None of this payload is injected into ordinary agent runs. The injector toolkit carries the agent binary, the fallback tools (`git`, `gh`, `rg`, `fd`, `jq`, `curl`, `bwrap`) and a CA bundle — several gigabytes of scanner root filesystems no longer ride along with every run, and they are pulled only when a scan actually needs them.

Each run separately records the `ga-security` wrapper digest, the immutable scanner OCI index digest, the selected amd64/arm64 manifest digest, and the runtime Bubblewrap digest (or the architecture-specific extracted binary digest for standalone tools). None can be supplied through the agent environment. Knowledge-driven tools additionally require compiled content digests for reviewed Nuclei templates, Wycheproof/RFC/NIST vectors, Zeek policy, and Suricata rules. Registry validation rejects missing, malformed, or mutable pins. There are deliberately no placeholder digests or automatic update channels in source.

## Agent execution through `run_security_tool`

A scan never runs a scanner itself. The agent calls the typed `run_security_tool` tool, and the platform takes over:

1. **Agent call.** Inputs are the registered tool name, a typed target (type, locator, revision, and — for workspace content — a staged object key with its sha256 digest and media type), typed registry arguments, an authorized `scope`, an optional `seed`, and the argument names whose values must be redacted. There is deliberately no image, command, argv, or raw-flag input: a model cannot choose what executable runs or how it is invoked.
2. **SecurityToolRun.** The call creates a `SecurityToolRun` (`platform.gratefulagents.dev/v1alpha1`) owned by the requesting AgentRun. Its spec is immutable, so a request cannot be rewritten after admission.
3. **Kubernetes Job.** The controller resolves the digest-pinned `security-tools` image, builds fixed argv from the compiled registry, and creates a short-lived Job that runs `ga-security`. Content from the agent workspace is staged through object storage and materialized in the Job; the Job refuses to run when the staged archive does not match the recorded sha256 digest.
4. **Result.** The Job writes `result.json` and the raw artifacts back to object storage. The controller records the verdict on `status.result` and the tool call returns it to the agent, which sees the status, finding count, coverage, and artifact references — not a shell transcript.

`status.phase` (`Pending`, `Running`, `Succeeded`, `Failed`) describes the Job; `status.result.status` is the scanner verdict. A `Succeeded` Job does not mean the target is clean, and a `Failed` Job never becomes a `pass`.

Because scanners frequently need to reach the target under test, the Job's egress is not restricted by the platform. The authorization boundary is the declared `scope`, which the registry and the tool wrapper enforce independently of model output — so scope must describe assets you are actually authorized to touch. Completed Jobs are removed by a TTL after they finish; the `SecurityToolRun` status and the object-storage artifacts remain the durable record.

Inside the Job, `ga-security` decodes a closed `RunConfig`, rejects unknown fields and disabled tools, runs argv directly with `exec.CommandContext` (never a shell), resolves external scanners only from the image and verifies their binary digest immediately before execution, checks file and canonical directory-tree digests, snapshots directory inputs, strips ambient credentials from the child environment, and enforces time and output limits. The status-specific exit codes are 0 pass, 10 findings, 20 partial, 30 not applicable, 124 timeout, and 1 error. Normalized findings flow into the existing persistence, deduplication, correlation, confidence, and report path.

Bash stays available to the agent for exploration, but the scanner binaries are not present in the run image, so exploratory Bash output is never a deterministic scan result.

## Operator configuration

The controller reads the image from the `SECURITY_TOOLS_IMAGE` environment variable. By default, the Helm chart derives the image from the manager image: it replaces the trailing `controller` repository name with `security-tools` and reuses `manager.image.tag`. Release automation publishes both images with the same version, so upgrading the manager also upgrades the scanner pack:

```yaml
manager:
  image:
    repository: ghcr.io/gratefulagents/controller
    tag: v0.7.117
```

This produces `ghcr.io/gratefulagents/security-tools:v0.7.117`. If the manager repository does not end in `controller`, set `agentImages.securityTools` explicitly. For production replay guarantees, override the derived tag with a digest and disable unpinned images:

```yaml
agentImages:
  securityTools: ghcr.io/gratefulagents/security-tools@sha256:...
securityTools:
  allowUnpinnedImage: false
```

The chart automatically permits its manager-derived release tag. Explicit image overrides remain digest-only by default; set `securityTools.allowUnpinnedImage: true` only when an explicit mutable tag is intentional.

The configured image is the trust anchor for every scanner argv, and it is recorded on each `SecurityToolRun` status as the image actually used. `Dockerfile.security-tools` is the reproducible build of that image. `security-tools.lock.json` records exact architecture-specific archive and extracted-binary hashes; the build-time Go installer verifies both before installing a binary and supports tar.gz, tar.xz, and zip without floating package indexes. An enabled entry may explicitly document an unsupported release architecture; the installer skips it only on that architecture, while undeclared missing artifacts still fail closed. Medusa 1.5.1 is therefore installed and enabled on amd64 and remains catalog-only on arm64 because upstream publishes no Linux arm64 asset. On amd64 it executes inside the pinned Slither/Crytic Compile compiler root, and its replay metadata binds the verified Medusa binary and compiler root as one closure. Entries that cannot be installed reproducibly are disabled with a reason. CI validates the lock and builds the image without contacting scan targets.

## Inputs and replay

Every run requires a target type, locator, immutable revision, and SHA-256 input digest. Domain-specific target types include base URLs/OpenAPI, authorization matrices, crypto vectors/binaries/models, TLS services, address scopes, pcaps, packet assertions, resettable protocol fixtures, Solidity projects, and Foundry security projects. The canonical digest of a source tree is computed when the target is staged and recorded on the `SecurityToolRun`; the Job recomputes it before the scanner sees the content. Live targets must also be contained by an explicit URL/host/address/prefix scope; a syntactically valid but unrelated scope is rejected. Tools with stochastic behavior require an explicit seed.

The replay record contains:

- target revision/digest and native-input artifact digests;
- exact tool version, architecture-specific release-artifact digest, and image/knowledge digests;
- canonical configuration and its digest;
- seed;
- a stable environment allowlist (`os`, `arch`, `kernel`, runtime/compiler/build, and assembly digest).

`MarshalCanonical` omits raw bytes and timestamps. Coverage and findings are sorted before serialization, so equivalent replay inputs have equivalent normalized JSON after documented native volatile fields have been discarded by adapters.

## Result semantics and artifacts

A result is exactly one of:

- `pass`: complete execution, no findings, no gaps or errors;
- `findings`: complete execution with normalized findings;
- `error`: execution/exit-code/normalization failure without useful findings;
- `timeout`: deadline or sandbox timeout;
- `partial`: useful output with errors, skipped assets, or uncovered assets;
- `not_applicable`: target type or required capability does not match.

A failure, unknown exit code, skipped asset, or coverage gap can never become `pass`. Coverage records independently list examined, skipped, and uncovered assets.

Native output is retained as a content-addressed artifact with media type, size, and SHA-256 digest. Binary pcaps remain separate artifacts. Every object for one run lives under `security-tool-runs/<namespace>/<name>/`: the staged `target.tar.gz`, and under `output/` the Job's `manifest.json`, the normalized `result.json`, and the `raw-NN` artifacts. `status.result.resultObjectKey`/`resultDigest` and each entry in `status.result.artifacts` (name, media type, size, digest, object key) point at them, and they outlive the Job and its TTL. Only redacted evidence is copied into finding/report fields. Authorization and proxy-authorization values, cookies, private keys, JWTs, and configured sensitive fields are removed. Raw artifacts can still contain sensitive packet or application content and therefore must use the same restricted artifact-store authorization and retention controls as scan source material.

Adapters produce `security.ScannerRecord` values. These feed the existing `NormalizeScannerRecord` → persistence/upsert → correlation/deduplication → confidence/ranking → Markdown/SARIF report pipeline. Each record cites its raw artifact digest in `extra.raw_artifact_digest` while preserving tool/version/rule provenance.

## Authorization matrix

The matrix input contains explicit cases with actor, actor tenant, resource, resource tenant, HTTP method/endpoint, operation, and expected/actual status. Cases can cover anonymous/authenticated access, roles, owner/non-owner resources, tenant boundaries, reads/mutations, fields/batches, alternate methods, and equivalent endpoints. An actual success where `401`, `403`, or `404` was expected creates `AUTHZ-MATRIX-UNEXPECTED-ALLOW` (`CWE-639`). Header names may appear in evidence but values never do.

The application harness is responsible for seeded identities/resources and reset hooks. The controller must count every expanded matrix case against request limits and mark unexecuted cases skipped or uncovered.

## Crypto specification

A machine-readable specification should state algorithm/mode, key sizes, nonce requirements, associated-data behavior, encodings, trust anchors, key lifecycle assumptions, and expected suites. Timing runs must retain compiler, build, assembly, OS, CPU/runtime context. dudect/ctgrind identify candidates only: statistical tests do not prove unpredictability, static patterns do not prove constant-time behavior, and a passing vector suite proves only conformance to those vectors.

## Network scope

Live discovery receives explicit addresses/prefixes, ports, protocols, rates, concurrency, and request limits. Naabu and Nmap accept only literal IP/CIDR targets, require an explicit validated port list, use unprivileged connect scans, and reject address/port combinations beyond the request budget. The sandbox must enforce this scope independently of model output. Nmap XML normalization excludes addresses outside the supplied target prefix; the run records them as skipped. Offline Zeek/Suricata/tshark analysis accepts a content-addressed pcap and OCI-root execution disables networking. ZAP plans reject substitutions, unknown job types, out-of-scope URLs, and reports not fixed to `/work/zap-report.json`. boofuzz requires a resettable fixture plus reset/health hooks; it must not target production.

Slither, Forge, Echidna, Medusa (amd64 only), and Halmos are a separate staged-build exception: they receive outbound access for compiler and project dependency resolution without treating the local staged path as a remote scan target. Their fixed registry invocations expose no caller-supplied RPC URL, raw scanner flags, or FFI. Other network tools still require explicit target scope and operator-authorized destinations.

## Blockchain and smart contracts

Echidna, Slither, and Medusa (amd64 only) accept canonical-digest Solidity project directories with media type `application/vnd.gratefulagents.solidity-project.v1+directory`. Echidna 2.3.0 uses an explicit seed, one worker, JSON output, 10,000 test sequences of length 32, and a 5,000-step shrink limit; Slither integration is disabled, and caller-selected RPC/FFI configuration is not accepted. A bounded campaign with no falsified property is evidence only about the staged harness and configured limits.

`forge-security-tests` accepts only media type `application/vnd.gratefulagents.foundry-security-project.v1+directory`, requires an explicit fuzz seed, and invokes the fixed `forge test --junit --threads 1` operation. Its child environment disables FFI while removing ambient RPC URLs and credentials. Forge may resolve compiler and project dependencies over the network, but the fixed invocation cannot inject a caller-supplied RPC endpoint. Foundry tests must encode the intended fuzz/invariant properties; a passing suite does not imply that unexpressed smart-contract properties are safe.

Slither 0.11.3 accepts a digest-verified Solidity project and compiles only from an ephemeral writable copy inside its OCI root. The pinned upstream toolbox currently embeds an amd64 `solc` artifact in its arm64 manifest, so arm64 Slither compilation fails closed as unsupported instead of attempting to execute the wrong architecture; the arm64 image still verifies the packaged root and Slither version. Halmos 0.3.3 accepts Foundry projects with existing symbolic tests and executes in a single-concurrency closure with fixed Z3, loop, width, and depth bounds. Slither, Forge, Echidna, Medusa (amd64 only), and Halmos have egress for compiler and project dependency resolution; none accepts caller-supplied RPC flags. Halmos exit code 1 is a counterexample; timeout, stuck, revert-all, and exception states are operationally incomplete and never findings or proofs. Missing compiler/dependency closures remain unsupported rather than passing.

Foundry can exercise repository-supplied local allocs or full-genesis fixtures through harnesses such as `vm.loadAllocs`; the workflow records their paths, digests, source chain/block/timestamp context, and state-completeness limitations. Remote `createFork`/`createSelectFork` RPCs, missing state, staged RPC caches, and raw Anvil persistence snapshots are unsupported. Live-chain RPC scanning, transaction submission, key custody, FFI, and exploitation are not supported.

## Offline fixtures and CI

`test/fixtures/security-toolpacks` contains:

- a two-tenant authorization matrix with a deterministic cross-tenant failure and a redacted Nuclei header-disclosure result;
- an RFC 4231 HMAC-SHA-256 passing known-answer output and reproducible failing output plus a crypto specification;
- a fixed pcap and prerecorded Zeek, Suricata, scoped Nmap, and Naabu native outputs using IANA TEST-NET addresses;
- a seeded failing Foundry invariant JUnit result for a local smart-contract project.

Unit tests use a fake sandbox and validate exact argv construction, pins, status precedence, redaction, coverage, replay equivalence, adapters, and flow into the existing security reporting pipeline. The security-tools image jobs additionally load the built amd64 and arm64 images and execute each packaged EVM runtime. These smoke tests use only local fixtures and never access public or production scan targets.

## Adding a wrapper

1. Add a `Tool` entry with an exact upstream version, fixed argv template, typed arguments/targets, native media type, requirements, budgets, status mapping, retry safety, and adapter name.
2. Build it into the reviewed OCI image; pin the resulting image digest at deployment. Add explicit reviewed knowledge-bundle digests where applicable.
3. Implement a deterministic adapter. Ignore documented volatile native fields, sort records by stable identity, cite the raw artifact digest, and redact report evidence.
4. Add committed native output and expected coverage under `test/fixtures/security-toolpacks`. Tests must cover pass/findings and error/partial behavior as applicable, plus replay equivalence.
5. Confirm the resulting `ScannerRecord` validates and appears in Markdown and SARIF through the existing finding pipeline.

Do not add a generic command, shell fragment, arbitrary environment map, automatic rules update, or unsafe retry path.
