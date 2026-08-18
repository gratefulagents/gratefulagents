# Protocol sandbox profiles

Evidence base for the protocol-family `SecurityWorkflow` assets in
`configs/securityworkflows/`. Facts are taken from the pinned in-scope repository
and its official build, test and harness configuration, not from recollection.

| File | Repositories profiled | Workflows it grounds |
| --- | --- | --- |
| `evm-defi-sandboxes.md` | `aave-dao/aave-v3-origin`, `lidofinance/core`, `sky-ecosystem/dss`, `sparkdotfi/spark-alm-controller`, `OlympusDAO/olympus-v3`, `1inch/limit-order-protocol`, `OffchainLabs/token-bridge-contracts` | `evm-lending-cdp-review`, `evm-orderbook-settlement-review` |
| `cross-chain-sandboxes.md` | `axelarnetwork/axelar-gmp-sdk-solidity`, `smartcontractkit/chainlink-ccip`, `hyperlane-xyz/hyperlane-monorepo`, `LayerZero-Labs/LayerZero-v2`, `wormhole-foundation/wormhole`, `matter-labs/zksync-era`, `Snowfork/snowbridge` | `cross-chain-messaging-review`, `rollup-stack-review` |
| `non-evm-sandboxes.md` | `Kamino-Finance/klend`, `near/core-contracts`, `near/intents`, `near-daos/sputnik-dao-contract`, `MystenLabs/sui`, `aptos-labs/aptos-core` | `solana-defi-program-review`, `near-contract-review` |
| `flow-cadence-sandboxes.md` | `onflow/flow-core-contracts`, `onflow/flow-evm-bridge` | `flow-cadence-review` |

Each profile records, per repository: the build system and its pins, the dependency
bootstrap a fresh clone actually needs, the canonical build and test commands taken
from the repository's own Makefile, package scripts or CI, the harness a proof of
concept should be written into, the fork or sandbox entry point with any block the
suite hard-codes, in-repo address manifests, and what blocks offline execution.

Three facts drive the workflow design and are easy to get wrong:

- The scan image ships **no** `forge`, `anvil`, `dapp`, `echidna`, `medusa`,
  `slither`, `nix`, `docker`, `tilt`, and **no Rust toolchain**. Every workflow
  therefore treats toolchain bootstrap as an explicit, recorded step that can fail,
  and requires a blocked lane to be reported rather than silently dropped.
- A shallow clone leaves several projects uncompilable: empty `lib/forge-std`
  submodules (Hyperlane, LayerZero, Snowbridge), soldeer dependencies (Olympus,
  Hyperlane), `node_modules` used as a Foundry library path (Arbitrum token bridge),
  and an **empty `contracts/` submodule in `zksync-era`**, which means a naive scan
  reviews zero on-chain Solidity.
- Only one repository in the catalog reaches live chain state from its default test
  path: `sputnikdao2/tests/utils/mod.rs` imports `sputnik-dao.near` from mainnet RPC.
  That path is network access and is permitted only when the scan's authorized
  network targets allow it.

Refresh a profile by re-cloning at the branch the `SecurityProgram` scan target pins
and re-reading the same files; record the retrieval date when you do.
