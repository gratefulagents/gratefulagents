# Provenance and applicability

These fixtures are original, minimal state machines derived from recurring mechanisms described in public primary advisories, incident reports, specifications, and audit reports. They do not reproduce project code, amounts, names, function names, or published finding labels. A citation supports a review question, not a claim that the synthetic fixture is equivalent to the cited system. Case-specific applicability and exclusions live only in `evaluator/manifest.json` so blind snapshots do not leak the answer.

| ID | Public primary source | Mechanism used in the benchmark |
|---|---|---|
| P01 | OpenZeppelin, [UUPSUpgradeable Vulnerability Post-mortem](https://forum.openzeppelin.com/t/uupsupgradeable-vulnerability-post-mortem/15680) | Initialization state and authority must be established before an exposed implementation can be used. |
| P02 | Coinbase, [Nomad Bridge incident analysis](https://www.coinbase.com/blog/nomad-bridge-incident-analysis) | Proof acceptance is insufficient when the accepted statement is not bound to the intended message context. |
| P03 | Cosmos IBC-Go, [GHSA-j496-crgh-34mx](https://github.com/cosmos/ibc-go/security/advisories/GHSA-j496-crgh-34mx) | Cross-domain lifecycle transitions require identity, replay, and mutually exclusive terminal-state reasoning. |
| P04 | Wormhole, [Incident Report: Signature Validation Vulnerability](https://wormhole.com/blog/incident-report-signature-validation-vulnerability) | Cryptographic validity must bind the data later consumed by the value transition. |
| P05 | Optimism, [2 Million Dollar Bug Bounty](https://www.optimism.io/blog/2-million-dollar-bug-bounty) | A forked execution client can diverge from upstream validation semantics; changed transitions must be compared against the upstream invariant. |
| P06 | ConsenSys Diligence, [Stop Using Solidity's transfer() Now](https://diligence.consensys.io/blog/2019/09/stop-using-soliditys-transfer-now/) | External execution before state commitment can observe or reuse stale accounting. |
| P07 | OpenZeppelin, [Reentrancy After Istanbul](https://blog.openzeppelin.com/reentrancy-after-istanbul) | Reentrancy safety is an ordering property across every callback-capable payout, including recovery paths. |
| P08 | Ethereum Improvement Proposal 712, [Typed structured data hashing and signing](https://eips.ethereum.org/EIPS/eip-712) | Signed authority requires explicit domain separation and replay boundaries. |
| P09 | Solidity documentation, [Non-standard Packed Mode](https://docs.soliditylang.org/en/latest/abi-spec.html#non-standard-packed-mode) | Adjacent dynamic values need unambiguous boundaries in authorization encodings. |
| P10 | OpenZeppelin, [Aave v3.0.1 audit](https://blog.openzeppelin.com/aave-v3-0-1-audit) | Asset decimal metadata and internal fixed-point scales must be reconciled at valuation boundaries. |
| P11 | Code4rena, [Basin audit report](https://code4rena.com/reports/2023-07-basin) | Integer rounding location and repeated operations can change conservation outcomes. |
| P12 | OpenZeppelin, [Compound III audit](https://blog.openzeppelin.com/compound-iii-audit) | Alternate entry points into one transition must enforce equivalent state predicates. |
| P13 | Sourcify, [How it works](https://docs.sourcify.dev/docs/how-to-verify/) | Source-level conclusions attach to deployments only after metadata and bytecode are matched. |
| P14 | Solana, [9/14 Network Outage Initial Overview](https://solana.com/news/9-14-network-outage-initial-overview) | Realistic adversarial load must be bounded before amplified per-item work threatens progress. |

The synthetic checks intentionally narrow each mechanism to one observable invariant. Reviewers must establish the preconditions in the manifest's `applicability` field before transferring a lesson to another target.
