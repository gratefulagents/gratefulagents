# Flow Cadence sandbox profiles

Verified 2026-08-18 against the official repositories selected by the
`hackenproof-flow-protocol` catalog entry. These profiles deliberately authorize
only repository tests and a local Flow emulator, never Flow mainnet, a public
testnet, or a deployed bridge.

## `onflow/flow-core-contracts` (`master`)

- The repository contains Cadence contracts, transaction templates, tests, and
  Go template bindings. Its documented baseline is `make test`; tests consume
  the transaction templates under `transactions/`.
- The security surface is a set of coupled state machines, not a generic token:
  FlowToken, storage and transaction fees, staking/delegation, locked tokens,
  epochs and the identity table, the service account, scheduled transactions,
  and random-beacon history.
- A reproducer belongs in the existing repository test lane. It must exercise
  the same transaction template or direct contract entry point implicated by
  the finding and include a negative control.

Official source: <https://github.com/onflow/flow-core-contracts>

## `onflow/flow-evm-bridge` (`main`)

- The repository is mixed Cadence, Go, and Solidity (`cadence/`, `flow.json`,
  `go.mod`, `solidity/`, and `foundry.toml`). `make test` runs the Cadence
  coverage lane and Go tests; the Makefile's Cadence command is
  `flow test --cover --covercode="contracts" --coverprofile="coverage.lcov" cadence/tests/*_tests.cdc`.
- Local deployment requires Flow CLI and Go. The documented path is `flow
  emulator`, followed in another process by `go run main.go`; success ends with
  the bridge deployed and unpaused in the local emulator.
- Every bridge request in either direction is initiated by a Cadence transaction
  acting through a Cadence-owned EVM account. Cadence-to-EVM onboarding may
  deploy the representation in the same transaction, while EVM-to-Cadence
  onboarding needs an earlier transaction because the new Cadence contract is
  unavailable until deployment completes.
- Conservation depends on native-asset provenance: Cadence-native assets are
  escrowed or represented on EVM, EVM-native assets are escrowed or represented
  in Cadence, and bridge-owned representations are burned on return. Tests must
  cover FT/NFT type binding, decimals and rounding, custom associations, pause
  state, opt-out before onboarding, failure rollback, and both directions.
- The published security model intentionally treats an attacker-controlled
  non-standard token as untrusted. A bridge finding must cross type isolation or
  corrupt a legitimate asset; breaking only the malicious token's own accounting
  is not a bridge vulnerability.

Official sources: <https://github.com/onflow/flow-evm-bridge> and
<https://github.com/onflow/flow-evm-bridge/blob/main/Makefile>
