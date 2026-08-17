# Cross-chain / L2 / ZK bug-bounty repo profiles

Recon date: this run. All clones under `/workspace/scratch/clones/<name>` (shallow, `--depth 1`;
`--filter=blob:none` for hyperlane, wormhole, zksync-era). `/workspace/repo` untouched.

## Sandbox toolchain inventory (`which`)

| tool | path |
|---|---|
| git | `/usr/bin/git` |
| node / npm / pnpm / yarn | `/usr/bin/node`, `/usr/bin/npm`, `/usr/bin/pnpm`, `/usr/bin/yarn` |
| cargo / rustc | `/usr/local/cargo/bin/{cargo,rustc}` |
| go | `/usr/local/go/bin/go` |
| kubectl | `/usr/local/bin/kubectl` |
| python3 / jq / rg | present |
| **forge / cast** | **MISSING** |
| **docker** | **MISSING** |
| **tilt** | **MISSING** |
| **nix** | **MISSING** |

Host: 16 vCPU, 61 GB RAM, 149 GB free on `/workspace`.

**Consequence up-front:** foundry is the single most load-bearing missing tool — 5 of 7 repos
(ccip, hyperlane, layerzero, wormhole, snowbridge) have their entire on-chain test suite behind
`forge test`. Docker/nix/tilt absence kills every multi-chain devnet (wormhole tilt, zksync
zkstack, snowbridge local testnet, ccip devenv).

## Repo scale (excluding `lib/`, `node_modules/`)

| repo | rev | size | .sol | .rs | .go | .ts | .move |
|---|---|---|---|---|---|---|---|
| axelar-gmp-sdk | 30196ca | 2.7M | 149 | 0 | 0 | 0 | 0 |
| chainlink-ccip | fe5c91c | 95M | 471 | 129 | 4622 | 19 | 0 |
| hyperlane | bd4e5f0 | 82M | 336 | 909 | 0 | 1973 | 0 |
| layerzero-v2 | 9c741e7 | 21M | 203 | 232 | 0 | 127 | 566 |
| wormhole | 097a235 | 100M | 105 | 274 | 640 | 653 | 112 |
| zksync-era | fa3a9b9 | 107M | 38* | 2059 | 0 | 145 | 0 |
| snowbridge | ac97538 | 87M | 114 | 98 | 177 | 151 | 0 |

\* zksync's real Solidity lives in the **unfetched `contracts/` submodule** (era-contracts).

---

# 1. axelar-gmp-sdk-solidity

### Component map
Pure on-chain SDK/library repo — **no off-chain component in tree**.

- `contracts/gateway/` — `AxelarAmplifierGateway`, auth/weighted-signer verification
- `contracts/executable/`, `contracts/express/` — GMP receive + express (pre-confirmation) execution
- `contracts/governance/`, `contracts/upgradable/`, `contracts/deploy/` (Create3), `contracts/libs/`
- `contracts/test/` — mock harness contracts (`DestinationChainSwapExpress.sol`, etc.)
- `test/{GMP,gateway,governance,upgradable,deploy,...}` — JS/mocha

### Toolchain
Hardhat only, npm (no foundry, no submodules).
`package.json`:
```json
"build": "npx hardhat clean && npx hardhat compile && npm run copy:interfaces",
"test": "npx hardhat test",
"lint": "solhint 'contracts/**/*.sol' && eslint 'scripts/**/*.js' 'test/**/*.js'",
"coverage": "cross-env COVERAGE=true hardhat coverage"
```
`hardhat.config.js` — solc `0.8.23` default (some contracts pinned `0.8.19` for deterministic
bytecode), `optimizer.runs = 1000000`, `evmVersion = process.env.EVM_VERSION || 'london'`.

### Exact commands (from `.github/workflows/test.yaml`)
```
npm ci
npm run build            # CI additionally fails on any "error"/"warning" in build.log
CHECK_CONTRACT_SIZE=true npm run test -- --parallel
```
Multi-EVM matrix: `npm run test-evm-versions` (`scripts/test-evm-versions.sh`).

### Devnet / multi-chain simulation
None. Cross-chain is simulated **in-process**: two gateway instances on one Hardhat EVM.
`test/GMP/GMP.js`:
```js
const sourceChain = 'chainA';
const destinationChain = 'chainB';
```
with `sourceChainGateway` / `destinationChainGateway` deployed side-by-side and the "relayer"
being the test calling `approveContractCall` + `execute` with a random command id
(`const getRandomID = () => id(Math.floor(Math.random() * 1e10).toString());`).

### Fork tests
None. No `createSelectFork`, no `rpc_endpoints`, no RPC env vars.

### Pitfalls
- `hardhat.config.js` does `readJSON(<dirname>/keys.json)` — **`keys.json` is absent from the
  clone**; needs a stub (`{}`) or the config throws before compile.
- Also imports `@axelar-network/axelar-chains-config/info/${ENV}.json` → network list comes from a
  package, so `npm ci` is mandatory before anything, including `hardhat compile`.
- CI treats compiler *warnings* as build failure — any injected instrumentation will red the build.
- Small, fast, fully offline after `npm ci`. Best candidate of the seven.

---

# 2. chainlink-ccip

### Component map
Root Go module `github.com/smartcontractkit/chainlink-ccip` is the **off-chain OCR3 plugin**, not
contracts (README: "This repository contains the core CCIP offchain protocol").

- Off-chain: `commit/`, `execute/`, `internal/`, `pkg/`, `plugintypes/`, `pluginconfig/`,
  `rmn_offchain.proto` (RMN "curse" oracle protocol)
- On-chain EVM: `chains/evm/contracts/` — `Router.sol`, `onRamp/`, `offRamp/`, `FeeQuoter.sol`,
  `pools/`, `ccvs/` (CommitteeVerifier), `rmn/`, `tokenAdminRegistry/`, `executor/`, `invariants/`
- On-chain SVM: `chains/solana/contracts/` — Anchor workspace (`programs/*`, `crates/*`)
- `deployment/` — separate Go module (CLDF deploy/changeset library, `testhelpers/`, `lanes/`)
- `devenv/` — local multi-chain dev environment (own Go module + `Justfile`)
- `integration-tests/` — own Go module

### Toolchain
Go 1.26.5 (root `go.mod`), **4 nested Go modules** (root, `deployment`, `devenv`,
`integration-tests`), foundry for EVM, Anchor 0.29.0 for Solana, pnpm for solidity npm deps.

Root `Makefile`:
```
build: go build -v ./...
test:  go test -race -fullpath -shuffle on -count $(TEST_COUNT) -coverprofile=$(COVERAGE_FILE) \
         `go list ./... | grep -Ev 'chainlink-ccip/internal/mocks|.../mocks|.../ocrtypecodecpb|.../chains'`
lint:  golangci-lint run -c .golangci.yml
```
(`TEST_COUNT ?= 10` — the default runs every Go test 10x.)

`chains/evm/GNUmakefile`:
```
test:     FOUNDRY_PROFILE=ccip forge test
snapshot: FOUNDRY_PROFILE=ccip forge snapshot --nmt "test?(Fuzz|Fork|.*_RevertWhen)_.*"
foundry:  foundryup --install v1.7.1
pnpmdep:  pnpm i
```
`chains/evm/foundry.toml` — solc `0.8.26`, `evm_version = 'paris'`, `libs = ["node_modules"]`,
`deny = 'warnings'`, deterministic env (`gas_price = 1 gwei`, `block_timestamp = 1785456000`,
`block_number = 23_500_000`), profiles `ccip` (runs=1) / `ccip-compile` (runs=80000, `via_ir`).

`chains/solana/Makefile`:
```
rust-tests:       cd ./contracts && cargo test
clippy:           cd ./contracts && cargo clippy -- -D warnings
anchor-go-gen:    ... anchor build ...   (ANCHOR_IMAGE ?= backpackapp/build:v0.29.0 — docker)
```

CI: `.github/workflows/solidity-foundry.yml` runs with `FOUNDRY_PROFILE: ccip`,
`working-directory: chains/evm`, plus `forge fmt --check`, `pnpm solhint`, coverage, sizes.

### Devnet / multi-chain simulation
`devenv/README.md`: "x2 Anvil chains, JobDistributor, NodeSet (4 nodes)".
```
brew install just
just build-jd-docker      # clones git@github.com:smartcontractkit/job-distributor (SSH, private)
just cli ; ccip sh
# tests
cd devenv/tests/e2e && go test -v -run TestE2ESmoke
go test -v -run TestE2ELoad/{clean,rpc_latency,gas,reorgs}
```
Hard requirements: **docker (compose)**, `just`, an SSH-authenticated clone of a *private* repo for
the job-distributor image, optional Loki/Grafana observability stack (`LOKI_URL`). Env selection via
`devenv/env*.toml` (`env-anvil.toml`, `env-geth.toml`, `forked-env.toml`, `env-cl-ci.toml`).

### Fork tests
`rg 'createSelectFork' -g '*.sol'` → **zero hits**; no `[rpc_endpoints]` in `chains/evm/foundry.toml`.
Fork-ish work is `devenv/forked-env.toml` (off-chain, docker-based), not forge.

### Message-lifecycle conventions
`chains/evm/contracts/test/e2e/` — `e2e.t.sol`, `e2e.cctp.t.sol`, `e2e.lombard.t.sol`,
`e2e.factoryDeployedPool.t.sol`, `e2e.feeWithdraw.t.sol`. Pattern: one test contract inherits both
sides of the lane and wires them in `setUp`:
```solidity
contract e2e is OnRampSetup, RouterFixture {
  Router.OnRamp[] memory onRampUpdates = ...;
  onRampUpdates[0] = Router.OnRamp({destChainSelector: DEST_CHAIN_SELECTOR, onRamp: address(s_onRamp)});
```
Shared fixtures: `BaseTest.t.sol`, `RouterFixture.t.sol`, `TokenSetup.t.sol`, `TokenFixture.t.sol`,
`test/helpers/OffRampHelper.sol`, `test/mocks/MockVerifier.sol`, `MockE2EUSDCTransmitter.sol`,
`MockE2ELBTCTokenPool.sol`. There is also `contracts/invariants/` and `test/attacks/`.

### Pitfalls
- 95 MB, 4622 Go files; `make test` default `-count 10 -race` is very expensive — always override
  `TEST_COUNT=1`.
- Multi-module Go: `go test ./...` at root does **not** cover `deployment/`, `devenv/`,
  `integration-tests/`.
- EVM tests need `pnpm i` in `chains/evm` first (`libs = ["node_modules"]`) and foundry pinned to
  `v1.7.1`; `deny = 'warnings'` makes any added warning fatal.
- Solana build path is docker (`backpackapp/build:v0.29.0`) → unavailable here; `cargo test` in
  `chains/solana/contracts` is the offline-friendly subset (still needs solana/anchor crates).
- `go 1.26.5` in `go.mod` may exceed the sandbox's Go version — check before assuming builds.

---

# 3. hyperlane-monorepo

### Component map
- On-chain EVM: `solidity/contracts/` — `Mailbox.sol`, `isms/`, `hooks/`, `token/` (warp routes),
  `middleware/` (InterchainAccount/Query), `client/`, `libs/`, `avs/`, `upgrade/`,
  `CheckpointFraudProofs.sol`
- On-chain SVM: `rust/sealevel/` (separate cargo workspace, native Rust programs)
- On-chain Starknet: `starknet/`
- Off-chain agents: `rust/main/` cargo workspace — relayer, validator, scraper, `lander/`,
  `hyperlane-base/`, `chains/`, `agents/`
- Off-chain TS: `typescript/*` — `sdk`, `cli`, `infra`, `relayer`, `rebalancer`, `ccip-server`,
  `warp-monitor`, `forking-sdk`, chain-specific SDKs (cosmos/starknet/svm/radix/tron/aleo)

`rust/README.md`: "two Rust workspaces ... [main] The offchain agents workspace ... [sealevel]
Hyperlane smart contracts and tooling for the SVM. You can only run `cargo build` after `cd`-ing
into one of these workspaces."

### Toolchain
pnpm 11.20.0 workspace + turbo; foundry + hardhat + soldeer for solidity; two cargo workspaces
pinned by `rust-toolchain` (`rust/main` = **1.88.0**, `rust/sealevel` = **1.86.0**).

Root `package.json`: `"build": "turbo run build"`, `"test": "turbo run test --continue"`,
`"test:ci": "turbo run test:ci"`, `"lint": "oxlint -c oxlint.json"`.
`pnpm-workspace.yaml` uses **`catalogMode: strict`** and `minimumReleaseAge: 10080` (rejects
packages published <7 days ago) with `patchedDependencies` (typechain, node-fetch, bigint-buffer).

`solidity/package.json`:
```json
"deps:soldeer": "forge soldeer install --quiet || echo 'Warning: soldeer install failed...'",
"build": "pnpm version:update && pnpm hardhat-esm compile && tsc && ...",
"test": "pnpm version:exhaustive && pnpm hardhat-esm test && pnpm test:forge",
"test:forge": "pnpm fixtures && forge test -vvv --decode-internal --no-match-contract 'Everclear|Tron|ForkTest'",
"test:ci": "pnpm version:changed && pnpm test:hardhat && pnpm test:forge --no-match-test testFork && pnpm test:tron && forge build --sizes",
"test:fork": "sh -c '. ./.env.default && forge test --match-test testFork && forge test --match-contract ForkTest'",
"coverage": "pnpm fixtures && ./coverage.sh"
```
`solidity/foundry.toml` — solc `0.8.33`, `evm_version = cancun`, `libs = ["dependencies","lib"]`
(soldeer), `fs_permissions` read `../vectors` + write `./fixtures`, per-file
`compilation_restrictions` pinning `CrossCollateralRouter.sol` to 3599 runs for EIP-170.

### Devnet / multi-chain simulation
`rust/main/utils/run-locally` — the canonical E2E:
```
cargo run -r -p run-locally
```
Env knobs (from `main.rs` doc comment): `E2E_CI_MODE`, `E2E_CI_TIMEOUT_SEC` (default 10 min),
`E2E_KATHY_MESSAGES` (default 16).
What it actually starts (`src/main.rs`, `src/ethereum/mod.rs`):
```rust
let build_main = Program::new("cargo") ...
let start_anvil = start_anvil(config.clone());
let postgres = Program::new("docker") ...      // scraper DB
Program::new("pnpm").working_dir(workspace_path).cmd("install"/"clean"/"build")
Program::new("pnpm").working_dir(&ts_infra_path).cmd("deploy-ism"/"deploy-core"/...)
let anvil = Program::new("anvil").flag("silent").spawn("ETH", None);
Program::new("pnpm").cmd("kathy").arg("messages", ...)   // message spammer
```
Hard requirements: **anvil (foundry)**, **docker** (postgres for scraper), full `pnpm install` +
`pnpm build` of the whole TS monorepo, and pinned rust 1.88.0. Variants exist for
`cosmos/cosmosnative/sealevel/starknet/radix/tron` behind cargo features. `rust/docker-compose.yaml`
and helm charts exist for agent deployment.

### Fork tests + RPC env
`solidity/.env.default`:
```
export RPC_URL_MAINNET="${RPC_URL_MAINNET:-https://eth.drpc.org}"
export RPC_URL_OPTIMISM=... RPC_URL_POLYGON=... RPC_URL_ARBITRUM=... RPC_URL_BASE=...
```
consumed by `[rpc_endpoints] mainnet = "${RPC_URL_MAINNET}"` etc.
Fork tests: `test/token/AtomicLocalRebalancingBridge.fork.t.sol`, `TokenBridgeOft.fork.t.sol`,
`TokenBridgeCctp.t.sol`, `EverclearTokenBridge.t.sol`, `token/PredicateRouterWrapperFork.t.sol`.
Default `test:forge` **excludes** them (`--no-match-contract '...|ForkTest'`).

### Message-lifecycle conventions
`contracts/mock/MockMailbox.sol` is the in-EVM two-domain relay:
```solidity
contract MockMailbox is Mailbox {
  function addRemoteMailbox(uint32 _domain, MockMailbox _mailbox) external {...}
  function processNextInboundMessage() public payable {...}
}
```
`test/Messaging.t.sol` is the minimal lifecycle:
```solidity
originMailbox.dispatch(remoteDomain, TypeCasts.addressToBytes32(address(receiver)), bytes(_message));
remoteMailbox.processNextInboundMessage();
assertEq(string(receiver.lastData()), _message);
```
Plus `contracts/mock/MockHyperlaneEnvironment.sol` (paired environments), `contracts/test/Test*.sol`
(TestIsm, TestRecipient, TestMailbox, TestMerkleTreeHook, TestPostDispatchHook...), cross-chain
fixtures under `vectors/` (foundry `fs_permissions` reads `../vectors`) and generated `./fixtures`.

### Pitfalls
- `solidity/lib/forge-std` is a **git submodule and is empty** in a shallow clone; deps also come
  from **soldeer** (`[dependencies]` pulls forge-std 1.9.2, OZ 4.9.3, nitro-contracts, chainlink
  ccip 1.5.0, optimism, predicate, permit2 from git) → network required for `forge soldeer install`.
- `catalogMode: strict` + `minimumReleaseAge` + `patchedDependencies` means `pnpm install` is
  strongly pinned and will refuse ad-hoc dependency changes.
- Three separate build graphs (turbo/TS, `rust/main` 1.88.0, `rust/sealevel` 1.86.0) with a
  ~900-file rust tree; full rust build is heavy.
- `test` has a prerequisite (`pnpm fixtures` creates `./fixtures/{aggregation,multisig}`) —
  running bare `forge test` without it fails on fs writes.

---

# 4. LayerZero-v2

### Component map
Everything under `packages/layerzero-v2/`:
- `evm/protocol/contracts/` — EndpointV2, MessagingChannel/Composer/Context, MessageLibManager
- `evm/messagelib/contracts/` — SendUln301/302, ReceiveUln301/302, `uln/dvn/DVN.sol`,
  `Executor.sol`, `PriceFeed.sol`, fee libs, `readlib/`
- `evm/oapp/contracts/` — OApp, OFT, precrime, examples (OmniCounter)
- `solana/programs/` + `solana/libs/` (cargo workspace), `solana/anchor-latest/`
- `aptos/contracts/`, `initia/contracts/`, `sui/contracts/`, `iota/contracts/` (Move, 566 files)
- `ton/` (FunC + blueprint/jest)

**No off-chain code in-tree** — DVNs and Executors are described in README but their workers are
off-repo (only the on-chain `DVN.sol` / `Executor.sol` counterparts are here).

### Toolchain
Yarn 4.0.2 berry workspaces (`"workspaces": ["packages/**"]`), foundry per EVM package,
cargo+anchor for solana, `aptos`/`sui` CLIs for Move, blueprint+jest for TON.

Root: `"build": "$npm_execpath workspaces foreach --all run build"`, same for `test`/`clean`.
Each EVM package: `"build": "forge build"`, `"test": "forge test"`.
README: **`yarn && yarn build && yarn test`**.

Per-package `foundry.toml` (`evm/protocol`): `auto_detect_solc = true`, `optimizer_runs = 20_000`,
`libs = ['../../../../lib']`, remappings into `node_modules/` and
`allow_paths = ["../../../../.yarn/unplugged", "../../../../node_modules", ...]`
(oapp adds `../protocol`, `../messagelib`; oapp pins `solc = '0.8.22'`).

Non-EVM entry points:
- `aptos/contracts/run_tests.sh`: `find . -name 'Move.toml' -execdir sh -c '... aptos move test --dev'`
- `sui/contracts/build_and_test.mjs compile|test [contract_name]`
- `ton/package.json`: `"build": "blueprint build --all"`, `"test": "jest --verbose"`

CI (`.github/workflows/lint-build-test.yaml`) runs everything inside
`ghcr.io/layerzero-labs/devcon:1.1.11-bookworm` with `--privileged` and re-inits docker
(`umount /var/run/docker.sock; /usr/local/share/docker-init.sh`), `submodules: recursive`,
then `yarn install --immutable && yarn build && yarn test`.

### Devnet / multi-chain simulation
No devnet. Multi-chain is simulated purely in one EVM via `evm/oapp/test/TestHelper.sol`:
```solidity
contract TestHelper is Test, OptionsHelper {
  enum LibraryType { UltraLightNode, SimpleMessageLib }
  mapping(uint32 => mapping(bytes32 => DoubleEndedQueue.Bytes32Deque)) packetsQueue; // dstEid => dstUA => guids
  mapping(bytes32 => bytes) packets;      // guid => packet bytes
  mapping(uint32 => address) endpoints;   // eid => endpoint
```
Usage (`oapp/test/OmniCounter.t.sol`):
```solidity
uint32 aEid = 1; uint32 bEid = 2;
setUpEndpoints(2, LibraryType.UltraLightNode);
address[] memory uas = setupOApps(type(OmniCounter).creationCode, 1, 2);
```
i.e. N endpoints + DVN + Executor + PriceFeed all deployed in one forge VM, packets queued per
destination eid and delivered manually — a real message-lifecycle harness, offline.

### Fork tests
`rg 'createSelectFork|vm.createFork' -g '*.sol' packages` → **zero hits**. No RPC env vars.

### Pitfalls
- `lib/forge-std` is a submodule and is **empty** in this clone → `forge test` cannot resolve
  `forge-std/Test.sol` until submodules are fetched (CI uses `submodules: recursive`).
- Yarn berry with possible PnP (`allow_paths` references `.yarn/unplugged`) — foundry remappings
  point at `node_modules/`, so a plain `yarn install` (node-modules linker) is required.
- CI is docker-image-based (`devcon:1.1.11-bookworm`) and privileged; reproducing exact
  solc/toolchain outside it is on you (`auto_detect_solc = true` silently fetches solc versions).
- Move/Solana/TON components each need a wholly separate CLI (`aptos`, `sui`, `anchor`, blueprint) —
  none present here.

---

# 5. wormhole

### Component map
- On-chain EVM: `ethereum/contracts/` — `Implementation.sol`, `Governance.sol`, `Messages.sol`,
  `bridge/` (TokenBridge), `nft/`, `Shutdown.sol`, `Setup.sol`
- On-chain Solana: `solana/` (cargo workspace: `bridge/`, `modules/`, `migration/`)
- Other chains: `aptos/`, `sui/`, `near/`, `algorand/`, `cosmwasm/`, `terra/`
- Off-chain: `node/` — **guardiand** (Go), `node/pkg/{p2p,processor,watchers,governor,...}`;
  `clients/`, `sdk/` (Go + JS), `proto/` (buf)
- `Tiltfile` + `devnet/*.yaml` — k8s manifests for the whole multi-chain devnet
- `SECURITY.md`, `SAFETY_CRITICAL_CODE.md`, `audits/` — useful scope signals

### Toolchain
Root `Makefile` (production-oriented; header says "This is not meant, or optimized, for incremental
or debug builds. Use the devnet for development"):
```
generate:  cd tools && ./build.sh ; tools/bin/buf generate
node:      $(BIN)/guardiand           (CGO_ENABLED=1)
lint:      bash scripts/lint.sh lint
lint-rust: bash scripts/clippy.sh
```
`node/Makefile`: `test: go test -v ./...` (+ a 3 s fuzz of `FuzzMessagePublicationUnmarshalBinary`),
`test-fast: go test -short ./...`.

`ethereum/Makefile`:
```
dependencies: node_modules forge_dependencies
lib/forge-std:            forge install foundry-rs/forge-std@v1.13.0 --no-git
lib/openzeppelin-contracts: forge install openzeppelin/openzeppelin-contracts@0457042d... --no-git
build:      npm run build           # forge build + typechain ethers-v5
test:       test-forge test-identifiers
test-forge: forge test --no-match-test .*_KEVM
test-upgrade: ./simulate_upgrades
test-push0: forge build --extra-output evm.bytecode.opcodes ; grep -qr PUSH0 ./build-forge && fail
```
`ethereum/foundry.toml`: solc **0.8.4**, `evm_version = "istanbul"`, `optimizer_runs = 200`,
`src = contracts`, `test = "forge-test"` ("so that truffle doesn't try to build them"),
`out = build-forge`.

CI (`.github/workflows/build.yml`) exact lines:
```
tilt ci --timeout 45m0s -- --ci --namespace=$DEPLOY_NS --num=3
kubectl delete --namespace=$DEPLOY_NS service,statefulset,configmap,pod,job --all
make node
cd ethereum && make test-push0 && make test
cd clients/js && make install ; cd ethereum && make test-upgrade
cargo fmt --check --all --manifest-path solana/Cargo.toml
cargo check --workspace --tests --manifest-path solana/Cargo.toml
cargo clippy --workspace --tests --manifest-path solana/Cargo.toml
cd algorand && make test ; cd sui && make test-docker ; (aptos) make test-docker
cd cosmwasm && make test ; cd sdk/vaa && go test -fuzz FuzzCalculateQuorum -fuzztime 15s
```

### Devnet (the reference multi-chain harness)
`DEVELOP.md` — Tilt + Kubernetes:
```
minikube start --cpus=8 --memory=8G --disk-size=50G --driver=kvm2
minikube ssh 'echo fs.inotify.max_user_watches=524288 | sudo tee -a /etc/sysctl.conf && sudo sysctl -p'
kubectl config set-context --current --namespace=wormhole
tilt up            # tilt args -- --num=2 ; tilt down --delete-namespaces
```
Requirements stated in-repo: Go >= 1.25.10, golangci-lint >= 2.1.2, **Tilt >= 0.20.8**, a local k8s
(minikube >= 1.21 recommended, kvm2), docker/moby >= 19.03. Dev VM recommendation:
**"at least 16 vCPU, 64G of RAM and 500G of disk"**.
`Tiltfile` flags: `--num=N` guardians, and per-chain toggles `--solana --evm2 --aptos --sui --near
--algorand --terra2 --wormchain --btc --pythnet --ibc_relayer --query_server --ci_tests
--node_metrics --guardiand_governor --guardiand_debug`.
Manifests: `devnet/{eth-devnet,eth-devnet2,solana-devnet,sui-devnet,aptos-localnet,near-devnet,
algorand-devnet,terra2-devnet,wormchain,spy,query-server,tests,tx-verifier-*}.yaml`.

### Fork support
No `createSelectFork` in `ethereum/forge-test`. Forking is done **out of band** via
`ethereum/anvil_fork`:
```bash
DOCKER_ARGS="-p 8545:8545" ./foundry anvil --host 0.0.0.0 --base-fee 0 \
  --fork-url $(worm info rpc mainnet $CHAIN_NAME) \
  --mnemonic "myth like bonus scare over problem client lizard pioneer submit female collect"
```
Note `./foundry` is a **docker wrapper**, and `worm` (from `clients/js`) must be installed.
`simulate_upgrade`/`simulate_upgrades` drive `make test-upgrade` against those forks.

### Message-lifecycle conventions
- On-chain: `ethereum/forge-test/Implementation.t.sol` builds and signs VAAs in-test with the devnet
  guardian key (`guardianSetIndex`, `sigs[i].guardianIndex`, "Sign the hash with the devnet guardian
  private key"), then asserts `parsed.guardianSetIndex`. Also `Messages.t.sol`, `MessagesRV.t.sol`
  (+ `forge-test/rv-helpers` for the KEVM proofs described in `PROOFS.md`), `Governance.t.sol`,
  `Shutdown.t.sol`, `WormholeDelegatedGuardians.t.sol`.
- Off-chain: `sdk/vaa/{structs,payloads,governance,quorum}_test.go` — canonical VAA
  parse/serialize/quorum vectors, with fuzz targets.
- Full lifecycle: tilt devnet `devnet/tests.yaml` (`--ci_tests`) drives real publish→guardian→VAA→
  redeem across the deployed chains.

### Pitfalls
- 100 MB clone; the devnet compiles Solana/Rust — this is why the repo asks for 16 vCPU / 64 GB.
- `ethereum/lib/` contains only a README: forge deps are installed at build time by
  `forge install ... --no-git` → **network required**, and pinned to exact commits.
- solc 0.8.4 / istanbul is ancient; `test-push0` explicitly guards against newer-EVM bytecode.
- Several component test targets are `make test-docker` (sui, aptos) → docker-only.
- `make generate` rewrites `node/pkg/proto` via buf; CI asserts `git diff --exit-code` afterwards.

---

# 6. zksync-era

### Component map
- Off-chain / node (Rust, `core/Cargo.toml` workspace, ~2059 .rs): `core/bin/zksync_server`,
  `external_node`, `block_reverter`, `contract-verifier`, `snapshots_creator`; `core/node/*` —
  `state_keeper` (sequencer), `eth_sender`, `eth_proof_manager`, `da_dispatcher`, `da_clients`,
  `consensus`, `api_server`, `reorg_detector`, `consistency_checker`, `commitment_generator`,
  `gateway_migrator`, `proof_data_handler`, `vm_runner`, `metadata_calculator`
- Prover: `prover/` (separate cargo workspace + `rust-toolchain`), `airbender_prover_server/`
- CLI: `zkstack_cli/` (separate cargo workspace) — the orchestrator for everything local
- **On-chain: `contracts/` is a git submodule → `https://github.com/matter-labs/era-contracts.git`
  and is EMPTY in this clone.** Same for `proof-manager-contracts/`. The yarn workspace lists
  `contracts/l1-contracts`, `contracts/da-contracts`, `contracts/l2-contracts`,
  `contracts/system-contracts` — all currently absent.
- Integration/E2E: `core/tests/{ts-integration,revert-test,upgrade-test,recovery-test,
  gateway-migration-test,loadnext,vm-benchmark,highlevel-test-tools}`

### Toolchain
Rust `rust-toolchain` = **`nightly-2025-03-19`** (root; `prover/` and `zkstack_cli/` have their own),
yarn 1.22.19 workspaces, `cargo-nextest 0.9.109`, `sqlx-cli 0.8.1`, foundry-zksync
(`foundryup-zksync`), docker compose, postgres 18, reth v1.8.2.

`docs/src/guides/setup-dev.md` (verbatim essentials):
```
sudo apt-get install -y build-essential pkg-config cmake clang lldb lld libssl-dev libpq-dev ...
nvm install 20 ; npm i -g yarn ; yarn set version 1.22.19
cargo install cargo-nextest --version 0.9.109 --locked
cargo install sqlx-cli --version 0.8.1
curl -L .../install-foundry-zksync | bash ; foundryup-zksync
echo "export ZKSYNC_USE_CUDA_STUBS=true" >> ~/.bashrc     # non-GPU
git submodule update --init --recursive
```
`docs/src/guides/launch.md`:
```
zkstack containers        # docker compose: reth + postgres (+ zk env image)
zkstack ecosystem init
zkstack dev clean all
```
`docker-compose.yml` services: `reth` (`ghcr.io/paradigmxyz/reth:v1.8.2`), `postgres:18`,
`zk` (`ghcr.io/matter-labs/zk-environment:latest2.0-lightweight`).
`bin/ci_run` = `docker-compose -f $compose_file exec -T zk "$@"`; `bin/ci_localnet_up` =
`docker-compose --profile runner up -d --wait`.

CI (`.github/workflows/ci-core-reusable.yml`), exact:
```
ci_run zkstack dev contracts
ci_run run_retried zkstack contract-verifier init --zksolc-version=v1.5.10 --zkvyper-version=v1.5.4 \
      --solc-version=0.8.26 --vyper-version=v0.3.10 --era-vm-solc-version=0.8.26-1.0.2 --only --chain era
ci_run zkstack dev test rust                      # cargo nextest under the hood
ci_run cargo test --manifest-path ./core/Cargo.toml --release -p vm-benchmark --bench oneshot --bench batch
ci_run zkstack ecosystem init --dev --support-l2-legacy-shared-bridge-test true --verbose
ci_run zkstack server --uring --chain=legacy --components api,tree,eth,state_keeper,housekeeper,...
ci_run zkstack dev t loadtest -v --chain=legacy
ci_run zkstack chain gateway convert-to-gateway --chain gateway --ignore-prerequisites
```

### Devnet / multi-chain simulation
`zkstack` is the harness: `zkstack containers` → `zkstack ecosystem init` → `zkstack chain create`
(+ `--prover-mode no-proofs`, `--l1-batch-commit-data-generator-mode rollup`) → `zkstack server`.
The gateway flow (`zkstack chain gateway create-tx-filterer` / `convert-to-gateway`) gives a real
**L1 ↔ L2 ↔ settlement-layer (Gateway)** topology — this is the multi-chain simulation.
Hard requirements: docker + docker-compose, postgres, reth, ~nightly rust, foundry-zksync,
`ZKSYNC_HOME` set, and **submodules fetched** (`--update-submodules` is a zkstack flag).

### Fork / RPC
No forge fork tests in-tree (contracts are in the submodule). `launch.md` passes
`--l1-rpc-url=http://localhost:8545` (the local reth). `core/tests/ts-integration` connects to the
locally launched server, not to public RPC.

### Message-lifecycle conventions
`core/tests/ts-integration/tests/` — `l1.test.ts` (L1↔L2 deposits/withdrawals),
`interop-a.test.ts` / `interop-b.test.ts` (cross-chain interop, v29 upgrade), `l2-erc20.test.ts`,
`base-token.test.ts`, `fees.test.ts`, `prividium.test.ts`.
Helpers used: `waitForL2ToL1LogProof`, `waitForInteropRootNonZero`, `getGWBlockNumber`,
`L2_MESSAGE_VERIFICATION_ADDRESS`, `ArtifactL2MessageVerification`, `ArtifactL1BridgeHub`,
`FinalizeWithdrawalParams`, `TestMaster` / `RetryableWallet` (`../src`).
Run: `yarn ts-integration test` → `zk f jest --forceExit --verbose --testTimeout 150000`
(plus `fee-test`, `api-test`, `contract-verification-test` variants).
Also `core/tests/{revert-test,upgrade-test,recovery-test,gateway-migration-test}` for
reorg/upgrade/snapshot-recovery lifecycles.

### Pitfalls
- **Biggest blocker: `contracts/` submodule empty** → no L1/L2/system Solidity to analyze without
  a second clone of `matter-labs/era-contracts` at the pinned commit.
- Nightly-pinned rust (`nightly-2025-03-19`), three cargo workspaces, 2059 rust files → very long
  cold builds; `sqlx` offline data / a live postgres needed for `cargo test` in DAL crates.
- Every documented workflow goes through docker (`ci_run` is literally `docker-compose exec`).
- GPU/CUDA assumptions in prover paths (`ZKSYNC_USE_CUDA_STUBS=true` needed to avoid them).
- 107 MB repo before any build artifacts.

---

# 7. snowbridge

### Component map
README is explicit that the project spans two repos:
> "[Snowfork/polkadot-sdk]: The Snowbridge parachain and pallets live in a fork of the polkadot-sdk
> ... [Snowfork/snowbridge]: The rest ... contracts, off-chain relayer, end-to-end tests and
> test-net setup code."

- On-chain Ethereum: `contracts/src/` — `Gateway.sol` (+ `GatewayProxy`), `Agent.sol`,
  `AgentExecutor.sol`, `BeefyClient.sol`, `Verification.sol`, `Assets.sol`,
  `interfaces/IGateway.sol`
- **On-chain Substrate pallets are NOT in this repo** (they are in the polkadot-sdk fork)
- Off-chain: `relayer/` (Go module, `go.work` → `use ./relayer`) — beefy/parachain/beacon relays,
  `relays/`, `chain/`, `ofac/`, magefile build
- `control/`, `gas-estimator/`, `web/` (local testnet + TS packages), `smoketest/` (Rust E2E)

### Toolchain
**Nix flake is the source of truth**:
```
nix develop        # then: scripts/init.sh
```
`flake.nix` devShell buildInputs: `foundry-bin`, `go`, `go-ethereum`, `mage`, `revive`, `delve`,
`rustup`, `cargo-sweep`, `clang`, `gcc`, `protobuf`, `nodejs_22`, `pnpm`, `yarn`, `typescript`,
`python311`, `jq`, `direnv`, `ps` (for zombienet), etc.
Flake input: `foundry.url = "github:shazow/foundry.nix"` → foundry version is flake-pinned.

`contracts/foundry.toml`: solc **0.8.34**, `optimizer_runs = 200`, `memory_limit = 1073741824`,
`gas_limit = 8000000000`, `no_match_test = "testRegenerate*"`,
`fs_permissions` read-write `test/data`, and:
```toml
# Integration profile: for running fork tests (compile with lower optimization)
# Usage: FOUNDRY_PROFILE=integration forge test --match-contract ForkUpgrade
[profile.integration]
test = 'integration'
[profile.production]
via_ir = true
optimizer_runs = 20000
```
Commands (`contracts/README.md`):
```
forge test                              # unit tests, "fully offline and fast"
FOUNDRY_PROFILE=integration forge test  # fork tests, "require an internet connection"
forge coverage
```
Go relayer: `go.work` (go 1.23.0 / toolchain go1.23.4), mage-based (`relayer/magefile.go`),
`relayer/docker-compose.yml` + Dockerfile. Smoketest: Rust (`smoketest/run-v1-tests.sh`,
`run-v2-tests.sh`, `run-legacy-v1-tests.sh`, `make-bindings.sh`).

### Devnet / multi-chain simulation
`web/packages/test/README.md`:
> "The E2E tests run against local deployments of the parachain, relayer, the ethereum execution
> layer (geth) and the ethereum consensus layer (lodestar)."
```
nix develop ; direnv allow ; scripts/start-services.sh
# wait for "Testnet has been initialized"
# relaychain ws://127.0.0.1:9944, BridgeHub ws://127.0.0.1:11144, Statemine ws://127.0.0.1:12144
```
Logs: `{rococo-alice,bob,charlie}.log`, `bridgehub0{1,2}.log`, `statemine0{1,2}.log`,
`{beefy,parachain,beacon}-relay.log`, `/tmp/snowbridge/geth.log`, `/tmp/snowbridge/lodestar.log`.
Scripts: `deploy-{ethereum,polkadot,contracts,beacon-state}.sh`, `start-ethereum-nodes.sh`,
`start-mock-beacon.sh`, `start-forked-mainnet.sh`, `run-smoketests.sh`,
`generate-beefy-checkpoint.sh`, `force-beacon-checkpoint.sh`.
Hard requirement beyond nix: `scripts/build-binary.sh` **compiles the polkadot-sdk fork from
source**:
```bash
cargo build --release --bin polkadot --bin polkadot-execute-worker --bin polkadot-prepare-worker $features
```
i.e. a sibling checkout at `$polkadot_sdk_dir` plus a full substrate release build.

### Fork tests + RPC env
`contracts/integration/`: `ForkParachainProof.t.sol`, `ForkUpgrade202509.t.sol`,
`ForkUpgrade202603.t.sol`, `SubstrateMerkleProofAliasingFork.t.sol`, `SubstrateMerkleProofProd.t.sol`.
```solidity
string memory rpc = vm.envOr("MAINNET_RPC_URL", string("https://eth.drpc.org"));
vm.createSelectFork(rpc, FORK_BLOCK);
// ForkUpgrade*: hardcoded Tenderly virtual RPC
"https://virtual.mainnet.eu.rpc.tenderly.co/61589e0a-...";
vm.createSelectFork(FORK_RPC_URL, 23_432_697);
```
So: `MAINNET_RPC_URL` is the one real env var; two suites hardcode a Tenderly virtual testnet URL
(likely to rot), one hardcodes `https://ethereum-rpc.publicnode.com`.

### Message-lifecycle conventions + fixtures
Unit tests: `contracts/test/{GatewayV1,GatewayV2,Agent,Verification,VerificationParachainProof,
BeefyClient,BeefyClientAdvanced,MMRProof,MMRProof2,SubstrateMerkleProof*,ScaleCodec,SubstrateTypes,
Bitfield,SparseBitmap,Token,SnowbridgeL2Adaptor}.t.sol` + `test/data/` JSON fixtures.
Fixture provenance is documented and is **relayer-log-derived**:
> "Some of the unit tests require fixture data generated by a live deployment ... logging artifacts
> from the offchain relayers running in the E2E stack. BEEFY commitments & proofs extracted from
> `/tmp/snowbridge/beefy-relay.log` ... 1. Search for `Sent SubmitFinal transaction` ... 2. Copy
> into `test/data/beefy-commitment.json`"
(`no_match_test = "testRegenerate*"` — regeneration tests are excluded by default and write into
`test/data` via `fs_permissions`.)

### Pitfalls
- **4 empty submodules**: `contracts/lib/{forge-std,openzeppelin-contracts,prb-math,canonical-weth}`
  (pins recorded in `contracts/foundry.lock`, e.g. forge-std `v1.12.0` / `7117c90c...`) → `forge test`
  cannot even compile until they're fetched.
- Nix is the only supported env and is absent here; foundry itself comes from the flake, so version
  is flake-pinned.
- The substrate half of the protocol is in another repo — a "full protocol" analysis needs
  `Snowfork/polkadot-sdk` (branch `snowbridge`) too.
- E2E requires geth + lodestar + zombienet + a source-built polkadot — far beyond a sandbox.
- Fork suites depend on a private-ish Tenderly virtual RPC and on specific mainnet block heights.

---

# What an automated protocol-specific workflow can rely on

| repo | canonical build cmd | canonical test cmd | message-flow harness entry point | what blocks offline execution |
|---|---|---|---|---|
| axelar-gmp-sdk | `npm ci && npm run build` | `npm run test` (`npx hardhat test`) | `test/GMP/GMP.js`, `test/GMP/GMPE.js` — two gateways (`chainA`/`chainB`) in one Hardhat EVM | `npm ci` (registry); missing `keys.json` breaks `hardhat.config.js`; `@axelar-network/axelar-chains-config` needed at config load |
| chainlink-ccip | `go build ./...`; EVM: `cd chains/evm && pnpm i && FOUNDRY_PROFILE=ccip forge build` | Go: `TEST_COUNT=1 make test`; EVM: `cd chains/evm && make test` (= `FOUNDRY_PROFILE=ccip forge test`) | `chains/evm/contracts/test/e2e/e2e*.t.sol` on `BaseTest`+`RouterFixture`+`TokenSetup`; off-chain: `devenv/tests/e2e` (`go test -run TestE2ESmoke`) | **no forge**; `pnpm i` for `libs=["node_modules"]`; devenv needs docker + `just` + private job-distributor repo; go.mod wants Go 1.26.5 |
| hyperlane | `pnpm install && pnpm build` (turbo); solidity: `pnpm -C solidity deps:soldeer && pnpm -C solidity build` | `pnpm -C solidity test:forge` (needs `pnpm fixtures` first) / `pnpm test:ci`; rust: `cd rust/main && cargo test` | `solidity/test/Messaging.t.sol` + `contracts/mock/MockMailbox.sol` (`dispatch` → `processNextInboundMessage`); full: `cargo run -r -p run-locally` | **no forge/anvil**; empty `solidity/lib/forge-std` submodule + soldeer git deps (network); run-locally needs anvil + docker postgres + full pnpm build; rust pinned 1.88.0/1.86.0 |
| layerzero-v2 | `yarn && yarn build` (per-pkg `forge build`) | `yarn test` (per-pkg `forge test`) | `evm/oapp/test/TestHelper.sol` — `setUpEndpoints(2, LibraryType.UltraLightNode)` + `setupOApps(...)`, packet queue per dstEid; see `OmniCounter.t.sol`, `OFT.t.sol`, `PreCrimeV2.t.sol` | **no forge**; empty `lib/forge-std` submodule; yarn berry install; non-EVM needs `aptos`/`sui`/`anchor`/blueprint CLIs |
| wormhole | `cd ethereum && make dependencies && make build`; node: `make node` | `cd ethereum && make test` (`forge test --no-match-test .*_KEVM`); `cd node && make test-fast` | in-EVM: `ethereum/forge-test/Implementation.t.sol` (devnet-guardian-signed VAAs); VAA vectors: `sdk/vaa/*_test.go`; full: `tilt ci -- --ci --num=3` | **no forge/tilt/docker/k8s**; `ethereum/lib` is empty and populated by `forge install ...@<pin>` (network); devnet wants 16 vCPU/64 GB/500 GB; sui/aptos tests are `make test-docker` |
| zksync-era | `cargo build --manifest-path core/Cargo.toml` (nightly-2025-03-19); ecosystem: `zkstack ecosystem init` | `zkstack dev test rust` (nextest) — or `cargo nextest run --manifest-path core/Cargo.toml`; integration: `yarn ts-integration test` | `core/tests/ts-integration/tests/{l1,interop-a,interop-b,l2-erc20}.test.ts` with `waitForL2ToL1LogProof` / `waitForInteropRootNonZero`; needs a running `zkstack server` | **no docker** (every documented path is `docker-compose exec zk`); **`contracts/` submodule empty → no on-chain Solidity at all**; postgres + reth + sqlx + foundry-zksync + nightly toolchain |
| snowbridge | `nix develop` then `cd contracts && forge build` | `cd contracts && forge test` (unit, offline); `FOUNDRY_PROFILE=integration forge test --match-contract ForkUpgrade` (needs `MAINNET_RPC_URL`) | unit: `test/GatewayV{1,2}.t.sol` + `test/Verification*.t.sol` against `test/data/*.json` (relayer-log-derived); full: `web/packages/test/scripts/start-services.sh` + `smoketest/run-v2-tests.sh` | **no nix/forge**; 4 empty `contracts/lib/*` submodules (pins in `contracts/foundry.lock`); E2E needs geth+lodestar+zombienet+source-built polkadot-sdk fork; fork tests need mainnet/Tenderly RPC |

## Cross-cutting conclusions for workflow design

1. **Install foundry first, unconditionally.** `forge`/`anvil` unlock the on-chain test suites of
   ccip, hyperlane, layerzero, wormhole and snowbridge. Versions matter: ccip pins
   `foundryup --install v1.7.1`; snowbridge takes foundry from its nix flake; zksync needs the
   separate `foundry-zksync` fork.
2. **Fetch submodules / package deps as an explicit pre-step.** Shallow clones leave hyperlane,
   layerzero, snowbridge and zksync uncompilable. `git submodule update --init --recursive` +
   (`forge soldeer install` | `forge install` | `pnpm i` | `yarn`) is the real "step 0". zksync
   additionally needs `matter-labs/era-contracts` or there is *no* Solidity to review.
3. **Prefer the in-EVM two-domain harnesses for automated message-lifecycle analysis.** Four repos
   ship one and it runs offline in a single forge/hardhat VM:
   axelar `test/GMP/GMP.js`; hyperlane `MockMailbox.processNextInboundMessage()`;
   layerzero `TestHelper.setUpEndpoints(n, ...)`; ccip `contracts/test/e2e/e2e.t.sol`.
   Wormhole's equivalent is VAA construction inside `Implementation.t.sol`.
4. **Treat the real devnets as out of scope for a sandbox.** wormhole (tilt+k8s+minikube, 16 vCPU /
   64 GB / 500 GB documented), zksync (docker-compose + reth + postgres + zkstack), snowbridge
   (nix + geth + lodestar + source-built polkadot), ccip devenv (docker + private image) all fail
   here on missing docker/nix/tilt. Their *config files* are still valuable as ground truth for
   topology and for which components are in scope.
5. **Fork-test surface is small and well-marked.** Only hyperlane (`RPC_URL_{MAINNET,OPTIMISM,
   POLYGON,ARBITRUM,BASE}` in `solidity/.env.default`, `--match-test testFork` /
   `--match-contract ForkTest`) and snowbridge (`MAINNET_RPC_URL`, `FOUNDRY_PROFILE=integration`)
   use forge forking. ccip, layerzero, axelar and wormhole's forge suites have none — so a
   no-network automated run loses almost nothing there.
6. **Off-chain scope is where the bounty value hides and it is language-split:** wormhole guardian
   (Go, `node/`), ccip OCR3 plugin (Go, repo root), hyperlane agents (Rust, `rust/main`), snowbridge
   relayers (Go, `relayer/`), zksync sequencer/eth_sender/prover (Rust, `core/node`, `prover/`).
   Go components (wormhole `node`, ccip root, snowbridge `relayer`) are the cheapest to build and
   test in this sandbox — `go` is present and they need no docker for unit tests.
