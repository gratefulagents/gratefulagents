# Blockchain security workflow research benchmark

This document records the evidence baseline used to maintain the shipped blockchain security workflows. It is not a substitute for a program's current rules or a protocol's pinned specification. Re-check every source at review time because program scope, fork status, implementations, and toolchains change.

## Scope and outcome

The review covered all 16 blockchain-related workflows:

- EVM smart contracts and general blockchain protocols/clients
- bridges, L2s, rollups, and zero-knowledge systems
- Algorand, Aptos Move, Bitcoin/Lightning, Cairo/Starknet, Cosmos/IBC and ABCI, Solana/Anchor, Substrate/XCM, Sui Move, and TON
- MPC/threshold cryptography, wallets, and blockchain off-chain services

The benchmark found that the existing workflows already had strong architecture, invariant, specialist-review, safe-validation, coverage, remediation, and retest foundations. The principal cross-cutting gaps were inconsistent program-policy capture, insufficiently explicit researcher hypothesis and prior-art loops, incident-derived regression testing, differential/reference testing, quantitative impact evidence, harness-quality checks, and a uniform disclosure-quality gate.

The shared `blockchain-security-research-method` skill now supplies those requirements to every task in every blockchain workflow. Chain-specific workflows retain their own technical checks; chain-specific benchmark deltas are applied in those workflow objectives as the ecosystems evolve.

## Immunefi comparison

Immunefi's model is program-specific. A review must capture the selected severity-system version, the program's assets and impacts, whether Primacy of Impact or Primacy of Rules applies, PoC requirements, exclusions, prohibited testing, and publication policy. The generic classification is not sufficient to establish eligibility.

Important practices incorporated into the shared method:

- impact-first severity with prerequisites and demonstrated consequences, not a vulnerability-class label;
- local forks and isolated fixtures rather than mainnet/public-testnet testing;
- no production DoS, high-traffic automation, phishing, third-party testing, or unsolicited fund rescue;
- executable, deterministic PoCs when required;
- rejection of scanner-only, best-practice, known, privileged-only, centralization-only, and purely theoretical submissions unless program terms say otherwise;
- a dated prior-art and duplicate search;
- private reporting and program-specific embargo/publication handling.

Primary sources:

1. [Immunefi Rules](https://immunefi.com/rules/)
2. [Immunefi Severity Classification Systems](https://immunefi.com/severity-classification-systems/)
3. [Immunefi Vulnerability Severity Classification System v2.3](https://immunefi.com/immunefi-vulnerability-severity-classification-system-v2-3/)
4. [Immunefi Vulnerability Severity Classification System v2.2](https://immunefi.com/immunefi-vulnerability-severity-classification-system-v2-2/)
5. [Immunefi Responsible Publication Policy](https://immunefi.com/responsible-publication/)
6. [Immunefi PoC Templates](https://immunefi.com/blog/security-guides/immunefi-poc-templates/)
7. [Immunefi guidance against public-chain exploit testing](https://immunefi.com/blog/all/why-you-should-never-test-exploits-on-mainnet-or-public-testnets/)
8. [Immunefi Safe Harbor](https://immunefi.com/safe-harbor/)

## Ethereum Foundation comparison

The Ethereum bounty is not an EVM-contract bounty. It separately covers protocol/specification soundness, execution and consensus clients, client/spec compliance, cryptographic primitives, Solidity and Vyper compilers, the deposit contract, and named dependencies. Its severity thresholds use validator/network impact and trigger cost and must not be translated from Immunefi labels.

Practices incorporated into the shared and protocol workflows:

- pin chain, fork, specification revision, client version, and fixtures;
- distinguish a specification defect from an implementation defect;
- compare reference/specification behavior across independent clients;
- cover execution, consensus, and Engine API boundaries separately;
- quantify validators/nodes affected, split/finality behavior, trigger cost, and recovery;
- require a local PoC and current production/mainnet relevance;
- treat known/public issues and publicly exposed trusted APIs according to program rules.

Primary sources:

1. [Ethereum Bug Bounty Program](https://ethereum.org/en/bug-bounty/)
2. [Ethereum consensus specifications](https://github.com/ethereum/consensus-specs)
3. [Ethereum execution specifications](https://github.com/ethereum/execution-specs)
4. [Engine API specification](https://github.com/ethereum/execution-apis/tree/main/src/engine)
5. [Ethereum Improvement Proposals](https://eips.ethereum.org/)
6. [Ethereum fork history](https://ethereum.org/en/ethereum-forks/)
7. [Solidity deposit contract](https://github.com/ethereum/solidity-deposit-contract)
8. [C-KZG-4844](https://github.com/ethereum/c-kzg-4844)

## Researcher workflow comparison

Leading workflows converge on a loop rather than a checklist:

1. freeze scope, revision, production wiring, and governing rules;
2. build asset/authority, state-machine, and representation models;
3. derive falsifiable invariants;
4. rank entry points and form attacker hypotheses;
5. compare equivalent paths and independent representations;
6. validate locally with reproducible evidence;
7. measure semantic coverage and test harness quality;
8. search variants and prior art;
9. triage concrete impact and produce a minimal private report;
10. retest against a new immutable revision.

The shared method makes that loop uniform. The existing workflows' chain-specific skills supply the attack surface details.

Methodology and testing references:

- [Trail of Bits secure-contract development workflow](https://secure-contracts.com/development-guidelines/workflow.html)
- [Trail of Bits Echidna property testing](https://secure-contracts.com/program-analysis/echidna/introduction/how-to-test-a-property.html)
- [OWASP Smart Contract Security Testing Guide](https://scs.owasp.org/SCSTG/)
- [Smart Contract Security Field Guide](https://scsfg.io/)
- [OpenZeppelin upgradeable-contract guidance](https://docs.openzeppelin.com/upgrades-plugins/writing-upgradeable)
- [EIP-1967 proxy storage slots](https://eips.ethereum.org/EIPS/eip-1967)
- [EIP-7201 namespaced storage](https://eips.ethereum.org/EIPS/eip-7201)
- [Trail of Bits: Breaking Aave Upgradeability](https://blog.trailofbits.com/2020/12/16/breaking-aave-upgradeability/)
- [OpenZeppelin UUPS vulnerability postmortem](https://forum.openzeppelin.com/t/uupsupgradeable-vulnerability-post-mortem/15680)

## Cross-chain, rollup, and proof-system evidence

The bridge/L2/ZK workflow already traced end-to-end value and message lifecycles. The benchmark reinforced typed state machines, reorg/reset semantics, implementation differentials, honest-challenger economics, data availability and forced-inclusion liveness, and exact proof-statement/key binding.

References:

- [OP Stack derivation specification](https://specs.optimism.io/protocol/derivation.html)
- [OP fault dispute game](https://specs.optimism.io/fault-proof/stage-one/fault-dispute-game.html)
- [OP deposit specification](https://specs.optimism.io/protocol/deposits.html)
- [Arbitrum Nitro architecture](https://docs.arbitrum.io/how-arbitrum-works/inside-arbitrum-nitro)
- [L2BEAT risk framework](https://l2beat.com/scaling/risk)
- [STARK paper](https://eprint.iacr.org/2018/046)
- [PLONK paper](https://eprint.iacr.org/2019/953)
- [0xPARC ZK bug tracker](https://github.com/0xPARC/zk-bug-tracker)
- [IBC client semantics](https://github.com/cosmos/ibc/tree/main/spec/core/ics-002-client-semantics)
- [IBC channel and packet semantics](https://github.com/cosmos/ibc/tree/main/spec/core/ics-004-channel-and-packet-semantics)
- [IBC-Go security model](https://ibc.cosmos.network/main/ibc/security/)
- [IBC-Go reentrant timeout advisory](https://github.com/cosmos/ibc-go/security/advisories/GHSA-j496-crgh-34mx)

## Chain-specific currency notes

Research also identified fast-moving ecosystem details that must be discovered from the target's pinned version rather than hard-coded globally:

- Solana limits and runtime behavior vary with Agave/Solana releases; pin the feature set and program/runtime versions.
- Aptos and Sui require platform-specific object/resource, parallel-execution, package-upgrade, and prover/test treatment.
- Sui protocol limits should come from the target network's `ProtocolConfig`.
- TON reviews must include asynchronous message traces, bounce/action phases, gas/value forwarding, and the actual language/toolchain in use.
- Algorand inventory must include current Algorand Python/Puya and TypeScript/Puya-TS projects, not only TEAL/PyTeal; review clear-state behavior, LogicSig arguments, box MBR lifecycle, and compiled/deployed programs.

Selected official references:

- [Solana program security course](https://solana.com/developers/courses/program-security)
- [Aptos Move Prover](https://aptos.dev/build/smart-contracts/prover)
- [Sui protocol configuration](https://docs.sui.io/concepts/object-model)
- [TON contract security](https://docs.ton.org/contracts/techniques/security)
- [TON Blueprint testing](https://docs.ton.org/contracts/blueprint/testing/overview)
- [Algorand Python unit testing](https://dev.algorand.co/algokit/unit-testing/python/)
- [Algorand TypeScript unit testing](https://dev.algorand.co/algokit/unit-testing/typescript/)
- [Algorand transaction reference](https://dev.algorand.co/concepts/transactions/reference/)
- [Algorand LogicSig guidance](https://dev.algorand.co/concepts/smart-contracts/logic-sigs/)
- [Algorand inner transactions](https://dev.algorand.co/concepts/smart-contracts/inner-txn/)
- [Algorand box storage](https://dev.algorand.co/concepts/smart-contracts/storage/box/)

## Maintenance rule

When a blockchain workflow changes, reviewers should confirm that it still:

- references `blockchain-security-research-method`;
- preserves one final triage/report sink;
- denies unrestricted shell execution for repository review tasks;
- distinguishes examined, skipped, unsupported, inconclusive, and uncovered work;
- never claims that a tool, test, deployment comparison, or PoC ran without an execution artifact;
- preserves immutable revision, environment, seed/bounds, and replay evidence;
- states limitations and does not call a bounded clean run proof of safety.
