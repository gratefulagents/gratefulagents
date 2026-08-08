# Deterministic security tool packs

The `internal/securitytoolpacks` package is the controller-owned execution contract for web/API, cryptography, and network/protocol tools. It prevents an agent from choosing an executable or assembling a shell command. A scan selects a statically registered tool and provides only values accepted by that tool's typed argument declarations; the registry produces an argv vector and an immutable OCI digest for a sandbox executor.

This is an execution primitive, not evidence that a clean target is secure. Human approval workflows are intentionally not part of the contract. The configured target scope and budgets are authoritative.

## Registry and pins

`DefaultManifest` returns schema `security-tool-registry/v1` and declares the supported versions, accepted target/artifact types, fixed invocation, native media type, adapter, network/privilege requirements, limits, status mappings, retries, and redaction policy for:

- web/API: Playwright, OWASP ZAP, Schemathesis, RESTler, mitmproxy, SSLyze, Nuclei, and the authorization-matrix runner;
- cryptography: Wycheproof, RFC/NIST vectors, dudect, ctgrind, tlsfuzzer, differential tests, Tamarin, ProVerif, Verifpal, and OpenSSL inspection;
- network/protocol: Nmap, tshark, Zeek, Suricata, Scapy, boofuzz, and testssl.sh.

The catalog distinguishes executable entries from catalog-only entries. Catalog-only tools remain visible with a reason but cannot produce an invocation. The universally available v1 execution path enables only the built-in authorization-matrix and pinned crypto-vector adapters; external scanners become executable only when their exact binary and knowledge pins are present in a reviewed runtime lock. This avoids claiming support for a binary that is absent from the worker.

The release/deployment supplies the actual `sha256:` digest of the tool-pack OCI image. The injected `ga-security` wrapper falls back to hashing its own static executable when no OCI digest is available. Knowledge-driven tools additionally require content digests for reviewed Nuclei templates, Wycheproof/RFC/NIST vectors, Zeek policy, and Suricata rules. Registry validation rejects missing, malformed, or mutable pins. There are deliberately no placeholder digests or automatic update channels in source. A release must verify upstream artifacts while building the image and pass resulting provenance into `DefaultManifest`.

## Agent execution through Bash

`ga-security` is compiled into `Dockerfile.injector`, so it is on every worker's PATH. The agent may invoke it through the normal Bash sandbox:

```text
ga-security --config run-config.json --output .security-results/example
```

This hybrid keeps Bash available for exploration while making reportable scanner runs reproducible. `ga-security` decodes a closed `RunConfig`, rejects unknown fields and disabled tools, asks the registry to produce fixed argv, runs argv directly with `exec.CommandContext` (never a shell), checks file input digests, and enforces time/output limits. It writes `result.json` and restricted `raw-NN` artifacts. The status-specific exit codes are 0 pass, 10 findings, 20 partial, 30 not applicable, 124 timeout, and 1 error. Findings are then submitted through `ingest_scanner_results`, preserving the existing persistence, deduplication, correlation, confidence, and report path.

The standalone `Dockerfile.security-tools` is a digest-pinned release artifact. `security-tools.lock.json` records exact multi-architecture archive hashes; entries that cannot be installed reproducibly are disabled with a reason. CI validates the lock and builds the image without contacting scan targets.

## Inputs and replay

Every run requires a target type, locator, immutable revision, and SHA-256 input digest. Domain-specific target types include base URLs/OpenAPI, authorization matrices, crypto vectors/binaries/models, TLS services, address scopes, pcaps, packet assertions, and resettable protocol fixtures. Tools with stochastic behavior require an explicit seed.

The replay record contains:

- target revision/digest and native-input artifact digests;
- exact tool and image/knowledge digests;
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

Native output is retained as a content-addressed artifact with media type, size, and SHA-256 digest. Binary pcaps remain separate artifacts. Only redacted evidence is copied into finding/report fields. Authorization and proxy-authorization values, cookies, private keys, JWTs, and configured sensitive fields are removed. Raw artifacts can still contain sensitive packet or application content and therefore must use the same restricted artifact-store authorization and retention controls as scan source material.

Adapters produce `security.ScannerRecord` values. These feed the existing `NormalizeScannerRecord` → persistence/upsert → correlation/deduplication → confidence/ranking → Markdown/SARIF report pipeline. Each record cites its raw artifact digest in `extra.raw_artifact_digest` while preserving tool/version/rule provenance.

## Authorization matrix

The matrix input contains explicit cases with actor, actor tenant, resource, resource tenant, HTTP method/endpoint, operation, and expected/actual status. Cases can cover anonymous/authenticated access, roles, owner/non-owner resources, tenant boundaries, reads/mutations, fields/batches, alternate methods, and equivalent endpoints. An actual success where `401`, `403`, or `404` was expected creates `AUTHZ-MATRIX-UNEXPECTED-ALLOW` (`CWE-639`). Header names may appear in evidence but values never do.

The application harness is responsible for seeded identities/resources and reset hooks. The controller must count every expanded matrix case against request limits and mark unexecuted cases skipped or uncovered.

## Crypto specification

A machine-readable specification should state algorithm/mode, key sizes, nonce requirements, associated-data behavior, encodings, trust anchors, key lifecycle assumptions, and expected suites. Timing runs must retain compiler, build, assembly, OS, CPU/runtime context. dudect/ctgrind identify candidates only: statistical tests do not prove unpredictability, static patterns do not prove constant-time behavior, and a passing vector suite proves only conformance to those vectors.

## Network scope

Live discovery receives explicit addresses/prefixes, ports, protocols, rates, concurrency, and request limits. The sandbox must enforce this scope independently of model output. Nmap XML normalization excludes addresses outside the supplied target prefix; the run must record them as skipped. Offline Zeek/Suricata/tshark analysis accepts a content-addressed pcap. boofuzz requires a resettable fixture plus reset/health hooks; it must not target production.

## Offline fixtures and CI

`test/fixtures/security-toolpacks` contains:

- a two-tenant authorization matrix with a deterministic cross-tenant failure;
- an RFC 4231 HMAC-SHA-256 passing known-answer output and reproducible failing output plus a crypto specification;
- a fixed pcap and prerecorded Zeek, Suricata, and scoped Nmap native outputs using IANA TEST-NET addresses.

Unit tests use a fake sandbox, so CI never downloads images or accesses public/production targets. They validate exact argv construction, pins, status precedence, redaction, coverage, replay equivalence, adapters, and flow into the existing security reporting pipeline.

## Adding a wrapper

1. Add a `Tool` entry with an exact upstream version, fixed argv template, typed arguments/targets, native media type, requirements, budgets, status mapping, retry safety, and adapter name.
2. Build it into the reviewed OCI image; pin the resulting image digest at deployment. Add explicit reviewed knowledge-bundle digests where applicable.
3. Implement a deterministic adapter. Ignore documented volatile native fields, sort records by stable identity, cite the raw artifact digest, and redact report evidence.
4. Add committed native output and expected coverage under `test/fixtures/security-toolpacks`. Tests must cover pass/findings and error/partial behavior as applicable, plus replay equivalence.
5. Confirm the resulting `ScannerRecord` validates and appears in Markdown and SARIF through the existing finding pipeline.

Do not add a generic command, shell fragment, arbitrary environment map, automatic rules update, or unsafe retry path.
