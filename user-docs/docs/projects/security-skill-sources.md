# Security skill sources and curation

Grateful Agents ships a curated security skill catalog for repository security scans. External skills are not copied into this repository: each `Skill` resource points to an immutable upstream commit and folder containing a `SKILL.md`. The controller validates the skill frontmatter before making its instructions available to runs.

## Workflow review

The shipped library currently contains 12 security workflows with 89 executable tasks. The review that introduced this catalog expansion found that specialist skills existed but were not attached to individual workflow tasks. Security workflow tasks now support `skillRefs`; deterministic task runs combine those references with the scan's default skills and reject missing references before dispatch.

Task-level selection is intentional. Loading the whole catalog into every run would waste context and mix unrelated guidance. For example, API authorization tasks load API authorization guidance, native memory-safety tasks load C/Rust and sanitizer guidance, and triage tasks load only validation and triage skills. Every shipped workflow task denies the `bash` tool, so operational guidance cannot compile or execute code from an untrusted repository; active analyzers remain isolated behind the typed security-tool runner.

## Imported sources

All imported references use full commit SHAs. The manifest description and source comments retain the provider and license.

### Trail of Bits

Source: [trailofbits/skills](https://github.com/trailofbits/skills)  
License: CC-BY-SA-4.0  
Pinned commit: `7b9bd5f950f89a9ba71b249b9801c1a95be3928e`

In addition to the existing audit, static-analysis, language-review, fuzzing, supply-chain, and blockchain skills, the expanded catalog adds the self-contained upstream skills that remain useful with the platform's single-file resolver:

- secure API design: `sharp-edges`;
- property-based testing;
- native fuzzing and memory safety: AddressSanitizer, AFL++, dictionaries, obstacle removal, and OSS-Fuzz guidance;
- cryptography testing: constant-time testing and Wycheproof;
- smart-contract process and maturity: guidelines advisor, secure workflow guide, and code maturity assessor;
- additional contract platforms: Algorand, Cairo/StarkNet, and TON.

Candidates were omitted when their essential workflow depended on colocated scripts or assets that the current resolver does not fetch. Being present upstream is not, by itself, sufficient for inclusion.

### Google

Source: [google/skills](https://github.com/google/skills)  
License: Apache-2.0  
Pinned commit: `092e210b243601797a0fb939040be2b1288e6d39`

The catalog includes `google-cloud-waf-security`, a structured review of the Google Cloud Well-Architected Framework security pillar. GKE platform/workload packages were reviewed but not imported because their current instructions include operational remediation and depend on additional files; repository scan tasks are read-only.

The Google SecOps detection-coverage package is also deferred. It requires a specific SecOps MCP integration that is not part of the default security runtime.

### GitHub Awesome Copilot

Source: [github/awesome-copilot](https://github.com/github/awesome-copilot)  
License: MIT  
Pinned commit: `3f0bba475ec40b9680e1d0311b9caffeec5ad4c3`

The catalog includes the reviewed `agent-governance` and `agent-supply-chain` skills. This repository is community-authored even though it is hosted by GitHub, so entries are reviewed individually rather than trusted by publisher name. The keyword-based `agent-owasp-compliance` entry is not included because keyword presence cannot establish compliance.

## Sources reviewed but not directly imported

The following sources remain useful research references or future integration candidates:

- **OWASP Cheat Sheet Series and Web Security Testing Guide** (CC-BY-SA-4.0): excellent methodology, including AI Agent Security, but not directly packaged as compatible skills. Adaptations must preserve attribution and ShareAlike terms.
- **Microsoft skills** (MIT): Entra agent identity and MCP builder guidance was reviewed. The former targets preview APIs and the latter is primarily implementation guidance, so neither was added to repository scan workflows in this tranche.
- **Google OSV-Scanner** (Apache-2.0): a high-value SCA tool, not an agent skill. It should be integrated as a pinned deterministic security tool with typed output.
- **Google OSS-Fuzz / ClusterFuzzLite** (Apache-2.0): services, infrastructure, and guides. The catalog uses Trail of Bits' focused OSS-Fuzz skill while tool execution remains a separate concern.
- **Mandiant capa and FLOSS** (Apache-2.0): strong static malware-analysis tools, but not repository-review skills. Any future pack must never execute submitted samples.
- **Bishop Fox CloudFox** (MIT), **Datadog security policies** (Apache-2.0), and **Wiz Open CVDB** (CC-BY-4.0): useful tool or reference data requiring dedicated integrations and evidence normalization.
- **NCC Group ScoutSuite** (GPL-2.0): deferred because redistributing adapted content would add material copyleft obligations.
- **Semgrep community rules**: not redistributed. The custom Semgrep Rules License restricts redistribution and service use.

No suitable, clearly redistributable skill package from Snyk or Palo Alto Unit 42 was verified during this review; the catalog does not invent provenance or infer permission.

## Safety and maintenance rules

External security skills must satisfy all of the following:

1. Use an authoritative public GitHub repository and an immutable 40-character commit SHA.
2. Point to a folder whose name matches the `SKILL.md` frontmatter name (a direct `SKILL.md` path is also supported).
3. State provider and license in the catalog description and preserve an upstream URL comment.
4. Remain useful when only `SKILL.md` is fetched. References to unavailable local scripts, templates, or data are a reason to omit the skill.
5. Respect scan-mode safety: repository scans are read-only, local proofs only, and must not mutate clusters, cloud resources, or production systems.
6. Avoid compliance claims unless the workflow gathers evidence sufficient for that claim.
7. Keep `configs/skills/` and `dist/chart/files/bootstrap/skills/` byte-identical. Run `make helm-sync-bootstrap` after changing shipped assets.
8. Re-review content, license, and dependencies before updating a pin.

## Remaining methodology gaps

High-value follow-up work includes dedicated skills for OAuth/OIDC/SAML/WebAuthn, general CI/CD security, OWASP/Google-informed AI-agent security, language-specific Go/JVM/.NET/JavaScript/Python review, cloud IaC workflows, and blockchain protocol topics such as consensus, rollups, bridges, P2P behavior, HSM/signing policy, and reorg recovery.
