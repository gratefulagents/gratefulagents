# Non-EVM bug-bounty repo profiles (recon, no builds run)

Cheap inspection only. Clones live in `/workspace/scratch/clones/<name>`.
Nothing in `/workspace/repo` was changed (a temp file was used to stage this report and removed).

## Sandbox toolchain inventory (this machine)

```
cargo        /usr/local/cargo/bin/cargo      (rustup shim)
rustc        /usr/local/cargo/bin/rustc      (rustup shim)
rustup       /usr/local/cargo/bin/rustup
solana       MISSING
anchor       MISSING
near         MISSING
cargo-near   MISSING
sui          MISSING
aptos        MISSING
movefmt      MISSING
node v22.23.2 / npm / yarn / pnpm / python3 / git / jq  present
```

**Critical:** `rustup toolchain list` -> `no installed toolchains`; `rustc --version` errors with
`rustup could not choose a version of rustc to run ... no default is configured`.
Zero Rust toolchains are installed - every repo below needs at least one network download before
any compilation. Disk: 149G free on `/workspace`.

Clone sizes (`--depth 1`; `--filter=blob:none --sparse` for sui/aptos):

| repo | worktree+git | .git |
|---|---|---|
| klend | 2.6M | 576K |
| near-core-contracts | 6.3M | 1.3M |
| near-intents | 7.8M | 1.7M |
| sputnik-dao | 2.1M | 540K |
| sui (sparse: crates/sui-framework, crates/sui-framework-tests, crates/sui-move, .github/workflows, scripts, docs/content/references) | 30M | 13M |
| aptos-core (sparse: aptos-move/framework, .github/workflows, scripts, docker) | 17M | 6.0M |

Note: full (non-sparse) checkouts of sui/aptos-core are multi-GB; plain `--depth 1 --filter=blob:none`
with a full checkout repeatedly stalled/aborted here. Sparse checkout is the reliable path.

---

## 1. klend - Kamino Lending (Solana / Anchor)

`/workspace/scratch/clones/klend` (branch `master`)

### Toolchain pins

- `rust-toolchain.toml`:
  ```toml
  [toolchain]
  channel = "1.74.1"
  ```
- `Anchor.toml` (entire relevant body):
  ```toml
  [programs.localnet]
  klend = "KLend2g3cP87fffoy8q1mQqGKjrxjC8boSyAYavgmjD"
  [registry]
  url = "https://api.apr.dev"
  [provider]
  cluster = "localnet"
  wallet = "/wallet.json"
  [scripts]
  test = "yarn run ts-mocha -p ./tsconfig.json -t 1000000 tests/**/*.ts"
  ```
  There is **no `anchor_version` / `solana_version` key** and **no `[test.validator]` section** -
  versions come from `Cargo.toml` only.
- `Cargo.toml` (workspace root):
  ```toml
  [workspace]
  resolver = "2"
  members = ["programs/*"]
  [workspace.dependencies]
  anchor-lang = { version = "0.29.0" }
  anchor-client = { version = "0.29.0" }
  anchor-spl  = { version = "0.29.0", features = ["dex","token","token_2022"] }
  solana-program = "~1.17.18"
  solana-sdk     = "~1.17.18"
  solana-banks-client = "~1.17.18"
  spl-token = { version = "3.5.0", features = ["no-entrypoint"] }
  borsh = { version = "0.10.3", features = ["const-generics"] }
  [profile.release]
  overflow-checks = true
  ```
  Plus two git `[patch.crates-io]` overrides (Kamino forks of `pythnet-sdk` and `spl-token-2022`) -
  these require network access to github.com even with a warm cargo registry cache.
- Second, independent workspace `libs/klend-interface/Cargo.toml` (a bare `[workspace]` at the
  bottom makes it its own root):
  ```toml
  rust-version = "1.81"
  solana-pubkey = "2.1"      # + solana-instruction = "2.1"
  [dev-dependencies]
  litesvm = "0.7"
  solana-sdk = "~2.3"
  solana-client = "~2.3"
  spl-token = "7"
  spl-associated-token-account = "6"
  ```
- CI (`.github/workflows/ci.yml`) pins: `dtolnay/rust-toolchain` stable for the program build,
  `toolchain: "1.86.0"` for `klend-interface` tests/clippy, `nightly-2026-01-01` for fmt.

### Build & test commands (from CI, authoritative)

```yaml
# build-klend job
- run: cargo install --locked solana-verify
- run: solana-verify build
- run: solana-verify get-executable-hash target/deploy/kamino_lending.so
# klend-interface-tests job (downloads the .so artifact into target/deploy first)
- working-directory: libs/klend-interface
  run: cargo test --lib
- working-directory: libs/klend-interface
  run: cargo test --test litesvm_integration
- run: cargo clippy --all-targets -- -D warnings
# fmt job
- run: cargo +nightly-2026-01-01 fmt --all -- --check
```
The Anchor path (`Anchor.toml [scripts].test`) is a vestigial stub: `tests/klend.ts` is the default
`anchor init` template calling `program.methods.initialize()` - not real coverage.
CI prerequisite: `apt-get install pkg-config build-essential libudev-dev`.

### Local sandbox / simulation

**LiteSVM** is the real harness - no validator, no RPC.
`libs/klend-interface/tests/litesvm_integration.rs` plus `tests/integration/`:
`setup.rs, pyth.rs, test_borrow_repay.rs, test_deposit.rs, test_flash_loan.rs, test_withdraw.rs,
test_multi_reserve.rs, test_ctoken_exchange_rate.rs, test_obligation_context.rs,
test_refresh_batch.rs, test_init.rs, test_from_account_data.rs, test_helpers.rs`.

`tests/integration/setup.rs:57-71`:
```rust
let mut svm = LiteSVM::new()
    .with_transaction_history(0) // Disable transaction history (allow transactions to be replayed)
    .with_lamports((100_000.0_f64 * 1_000_000_000.0) as u64);
...
// Load the klend program .so
let so_path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
    .join("../../target/deploy/kamino_lending.so");
let program_bytes = std::fs::read(&so_path)
    .unwrap_or_else(|e| panic!("Failed to read {}: {e}", so_path.display()));
svm.add_program(KLEND_PROGRAM_ID, &program_bytes).unwrap();
svm.add_program(FARMS_PROGRAM_ID, &program_bytes).unwrap();
```
**Hard dependency: the SBF `.so` must exist at `target/deploy/kamino_lending.so` before the
integration tests run.** CI produces it in the `solana-verify build` job and passes it as an
artifact - it is *not* committed.

No `solana-program-test`/`ProgramTest`, no `bankrun`, no `solana-test-validator` anywhere: a grep
across `*.rs *.ts *.toml *.json *.md` for those terms returns zero hits (only litesvm hits).

### Mainnet-state reuse

None. No `--clone`, no `[test.validator]` accounts, no committed account fixtures or snapshots.
State is synthesized: `setup.rs::inject_global_config()` computes the discriminator as
sha256 of "account:GlobalConfig" truncated to 8 bytes and writes an 8+1024-byte account;
`pyth.rs::create_pyth_price_account(svm, 1.0)` fabricates a mock Pyth price account. Sizes are
hardcoded (`LENDING_MARKET_SIZE = 8 + 4656`, `RESERVE_SIZE = 8 + 8616`,
`GLOBAL_CONFIG_SIZE = 8 + 1024`); `advance_clock_by_slots()` moves the `Clock` sysvar.

### Test/PoC conventions

- Rust integration tests under `libs/klend-interface/tests/integration/`, module-per-flow, with a
  shared `setup_full_env() -> TestEnv { svm, admin, lending_market, reserve, liquidity_mint, pyth_oracle }`.
- `tests/*.ts` is an anchor/ts-mocha stub - ignore.
- `programs/klend` cargo features worth flagging to a scanner: `staging`, `serde`, `idl-build`,
  `tracing`, `serialize_caps_interval_values`.

### Pitfalls for automation

- Needs rustup toolchains 1.74.1 **and** 1.86.0 **and** nightly-2026-01-01 (3 downloads).
- SBF build needs Solana platform-tools (`cargo-build-sbf`) or `solana-verify` (itself a
  `cargo install --locked` long compile, or Docker). Neither is present here.
- git `[patch.crates-io]` deps mean an offline cargo build is impossible without pre-vendoring.
- Two disjoint cargo workspaces: `cargo test` at the repo root will NOT pick up the litesvm tests;
  you must `cd libs/klend-interface`.
- Cheapest useful loop: prebuild and cache `kamino_lending.so`, then iterate on
  `cd libs/klend-interface && cargo test --test litesvm_integration` (in-process, seconds-scale).

---

## 2. near-core-contracts (NEAR, legacy)

`/workspace/scratch/clones/near-core-contracts` (branch `master`)

Contracts: `lockup`, `lockup-factory`, `multisig`, `multisig2`, `multisig-factory`,
`staking-pool`, `staking-pool-factory`, `voting`, `whitelist`, `w-near`, `state-manipulation`.

### Toolchain pins

- **No workspace root `Cargo.toml`** and **no `.github/workflows` directory at all** - there is no
  CI to mine for authoritative versions.
- Only one toolchain file, `state-manipulation/rust-toolchain`:
  ```toml
  [toolchain]
  channel = "nightly"
  ```
- near-sdk versions are old and heterogeneous (grep over `*/Cargo.toml`):
  ```
  lockup/Cargo.toml:12                 near-sdk = "3.1.0"
  lockup/Cargo.toml:16                 near-sdk-sim = "3.2.0"
  lockup-factory/Cargo.toml:22         near-sdk = "3.1.0"
  w-near/Cargo.toml:12                 near-sdk = "3.1.0"
  w-near/Cargo.toml:13                 near-contract-standards = "3.1.0"
  w-near/Cargo.toml:16                 near-sdk-sim = "3.1.0"
  multisig2/Cargo.toml:25              near-sdk = "4.0.0-pre.4"
  multisig2/Cargo.toml:28              near-sdk-sim = "3.2.0"
  multisig, voting, whitelist, staking-pool, staking-pool-factory, multisig-factory:
                                       near-sdk = "2.0.0"
  ```
  No `cargo-near` and no `near-workspaces` in any Cargo.toml.

### Build & test commands

Per-contract `build.sh`, e.g. `lockup/build.sh`:
```bash
RUSTFLAGS='-C link-arg=-s' cargo build --target wasm32-unknown-unknown --release
cp target/wasm32-unknown-unknown/release/lockup_contract.wasm res/
```
`multisig2/build.sh` and `multisig-factory/build.sh` use `cargo +stable build ...`.
`state-manipulation/build.sh` builds three feature variants (`--all-features`,
`--no-default-features --features clean`, `--no-default-features --features replace`).

Aggregators:
- `scripts/build_all.sh` - pipes `jq -c '.[]' scripts/contracts.json` into a while loop, extracts
  `.contract_dir` per entry with `jq -r`, runs each `./build.sh`; with `-c/--check` it asserts
  `git diff --exit-code` ("please make sure you have committed all contract wasm files").
- `scripts/test_all.sh` - same jq loop, then per contract dir:
  `RUSTFLAGS='-D warnings' cargo test`.
- `scripts/build_all_docker.sh` / `scripts/build_docker.sh` for reproducible wasm.

### Local sandbox / simulation

Two generations, both dated:
- **near-sdk-sim (in-process `RuntimeStandalone`)** - `lockup/tests/spec.rs:9-58,1579`:
  ```rust
  use near_sdk_sim::runtime::GenesisConfig;
  use near_sdk_sim::{deploy, init_simulator, to_yocto, UserAccount};
  near_sdk_sim::lazy_static_include::lazy_static_include_bytes! { ... }
  let root = init_simulator(Some(genesis_config));
  let lockup = deploy!( ... );   // spec.rs:58,102,309,513,854,1179
  ```
  Same pattern in `multisig2/tests/general.rs` (`RuntimeStandalone`, `StateRecord`, `AccessKey`,
  `near_crypto::InMemorySigner`) and `w-near/tests/mod.rs`.
- **`workspaces` (old crate name, pre-`near-workspaces`)** - `state-manipulation/src/lib.rs:118,181`:
  ```rust
  use workspaces::{prelude::*, Contract, DevNetwork, Worker};
  let worker = workspaces::sandbox().await?;
  ```
  `workspaces::sandbox()` downloads and starts a `neard` sandbox binary -> network required.

near-sdk-sim is fully in-process, so it is the only offline-capable NEAR harness in this set.

### Mainnet-state reuse

No account cloning, no chain snapshots. But **compiled wasm artifacts are committed** and used as
test fixtures - effectively frozen deployed-code snapshots:
```
lockup/res/lockup_contract.wasm            lockup/tests/res/fake_voting.wasm
multisig/res/multisig.wasm                 multisig2/res/multisig2.wasm
multisig-factory/res/multisig_factory.wasm lockup-factory/res/lockup_factory.wasm
staking-pool/res/staking_pool.wasm         staking-pool-factory/res/staking_pool_factory.wasm
voting/res/voting_contract.wasm            whitelist/res/whitelist.wasm
w-near/res/w_near.wasm                     w-near/res/legacy_w_near.wasm  <- legacy/upgrade fixture
state-manipulation/res/{state_manipulation,state_replace,state_cleanup}.wasm
```
Tests load them via `lazy_static_include_bytes!`, so **tests do not require a fresh wasm build**.

### Test/PoC conventions

`<contract>/tests/*.rs` for sim/integration; `<contract>/src/tests/` for unit tests;
`scripts/tests/` for shell checks; `scripts/deploy*` for mainnet ops recipes. The README documents
the atomic create-account + deploy + init pattern used by the lockup tests.

### Pitfalls for automation

- near-sdk 2.x / 3.x / 4.0.0-pre.4 will not compile on a modern rustc without pain; expect
  version-lock friction. `state-manipulation` demands unpinned `nightly` (a moving target).
- Requires the `wasm32-unknown-unknown` target (extra rustup download).
- No CI file means no known-good toolchain; you have to bisect.
- Highest-value cheap loop: `cargo test` per contract dir - the committed `res/*.wasm` means
  near-sdk-sim tests run without building wasm first.

---

## 3. near-intents / defuse (NEAR, modern)

`/workspace/scratch/clones/near-intents` (branch `main`)

### Toolchain pins

- `rust-toolchain`:
  ```toml
  [toolchain]
  channel = "1.97.1"
  components = ["clippy", "rustfmt"]
  targets = ["wasm32-unknown-unknown"]
  ```
- `Cargo.toml`: `resolver = "3"`, `edition = "2024"`, `rust-version = "1.95.0"`, roughly 60
  workspace members: `contracts/defuse{,/core}`, `contracts/poa/{factory,token}`,
  `contracts/wallet` plus signature backends (ed25519, no-sign, webauthn/{ed25519,p256}),
  `contracts/{escrow-swap,global-deployer,outlayer/app,treasury-logger}`,
  `crates/signatures/{erc191,nep413,nep461,nep641,webauthn,sep53,tip191,ton-connect}`,
  `crates/mpc/{ckd,kdf,signer}`, `crates/testing/{utils,randomness,sandbox}`, `tests`.
- Key deps:
  ```toml
  near-sdk = "5.29.0"
  near-sdk-env = "0.1.5"
  cargo-near-build = "0.11.5"
  near-kit = { version = "0.13", default-features = false }   # Cargo.lock:3905 -> near-kit 0.13.0
  ```
- Reproducible-build pin, `contracts/defuse/Cargo.toml:82`:
  ```toml
  [package.metadata.near.reproducible_build]
  image = "sourcescan/cargo-near:0.22.0-rust-1.97.1"
  image_digest = "sha256:7467038bdddc86484b73b416eeadce926ff59013e128e53dec5a19e1cb4b2234"
  passed_env = []
  container_build_command = [
    "cargo","near","build","non-reproducible-wasm","--locked","--features=contract", ...]
  ```
  plus a `.variant.far` variant with `--features=contract,far`.
- CLI pins from CI: `cargo-near v0.22.0` (installer script for the fast job,
  `cargo install cargo-near --version 0.22.0 --locked` for the reproducible job), `taplo 0.10.0`,
  `cargo-machete v0.9.2`, `cargo-audit =0.22.2`. apt: `libudev-dev`.

### Build & test commands

`Makefile`:
```make
test:              cargo test --all --all-features
check:             check-contracts ; cargo clippy --workspace --all-targets --all-features
check-fmt:         cargo fmt --all --check ; RUST_LOG=warn taplo format --check
check-unused-deps: cargo machete
check-all:         check-fmt check-unused-deps check
# Per-contract wasm targets are GENERATED at Makefile-parse time by piping
#   'cargo metadata --format-version=1' into jq over package.metadata.near.reproducible_build.
# Effective build: 'cargo near build reproducible-wasm' (REPRODUCIBLE=1)
#   or the container_build_command: 'cargo near build non-reproducible-wasm --locked --features=...'
# Per-contract clippy: RUSTFLAGS='--cfg=near' cargo clippy -p <crate> --target wasm32-unknown-unknown
# Artifacts land in ./res/<crate>.wasm plus <crate>.abi.json
```
`README.md`: `make CONTRACT[/VARIANT] [REPRODUCIBLE=1]`, `make`, `make test`,
`cargo integration-tests <defuse|poa|escrow-swap>`, and
"For state migration testing set environmental var `DEFUSE_MIGRATE_FROM_LEGACY=1`".
`.cargo/config.toml`:
```toml
[target.'cfg(all(near, target_family="wasm"))']
rustflags = ["--remap-path-prefix=.=src", "--remap-path-prefix=${HOME}=/"]
[env]
DEFUSE_USE_OUT_DIR = { value = "res", relative = true }
DEFUSE_OUT_DIR     = { value = "res", relative = true }
[alias]
integration-tests = "test -p defuse-tests --no-default-features --features"
```
CI `tests` job downloads the reproducible `res/` artifact then runs
`DEFUSE_MIGRATE_FROM_LEGACY=true DEFUSE_USE_OUT_DIR=res make test`.

**Gotcha: merely parsing the Makefile shells out to `cargo metadata`** - even `make help` needs a
working toolchain and a resolvable dependency graph.

### Local sandbox

`crates/testing/sandbox` (`defuse-sandbox`) wraps `near-kit` with the `sandbox` feature:
```rust
// crates/testing/sandbox/src/lib.rs:11,21
use near_kit::{Near, NearToken, sandbox::SandboxConfig};
SandboxConfig::shared()
```
`near-kit 0.13.0` (crates.io) is the modern successor to `near-workspaces`; it launches a `neard`
sandbox binary (downloaded on first use). Test crate `tests/` (`defuse-tests`) depends on
`defuse-sandbox`, `tokio`, `rstest`, `arbitrary`, `defuse-randomness`; features per subsystem
(`defuse, deployer, escrow-swap, imt, long, outlayer, poa`). Test tree:
`tests/src/tests/{defuse,escrow,global_deployer,outlayer_app,poa}` plus `utils.rs`.
Lineage confirmed by `tests/src/tests/defuse/tokens/nep245/mt_transfer_resolve_gas.rs:216`:
"This is necessary because near-workspaces fails if *any* of the receipts fail within a call."

### Mainnet-state reuse

No account cloning, no chain snapshots. Instead **state-migration testing**:
`DEFUSE_MIGRATE_FROM_LEGACY=1` builds pre-migration data, applies migrations, then verifies state
integrity against newly created data (README plus the CI `tests` job env). The `res/` wasm is the
carried artifact (gitignored; CI passes it between jobs).

### Pitfalls

- Rust 1.97.1 plus edition 2024 plus resolver 3 -> recent toolchain download required.
- `cargo near build` is a separate binary install; reproducible mode needs Docker and the
  digest-pinned `sourcescan/cargo-near` image -> not offline.
- `near-kit` sandbox downloads `neard` at test time -> not offline.
- `make test` (= `cargo test --all --all-features`) includes the integration suite, which expects
  `res/*.wasm` to already exist (`DEFUSE_USE_OUT_DIR=res`); build-before-test ordering is mandatory.

---

## 4. sputnik-dao-contract (NEAR DAO)

`/workspace/scratch/clones/sputnik-dao` (branch `main`)

### Toolchain pins

- `rust-toolchain.toml`:
  ```toml
  [toolchain]
  channel = "1.86"
  components = ["rustfmt"]
  targets = ["wasm32-unknown-unknown"]
  ```
- `Cargo.toml`: `resolver = "3"`, members `sputnik-staking, sputnikdao2, sputnikdao-factory2,
  test-token`; `[profile.release] codegen-units=1, opt-level="z", lto=true, panic="abort",
  overflow-checks=true, strip="symbols"`.
- `sputnikdao2/Cargo.toml`:
  ```toml
  edition = "2024"
  near-sdk = { version = "5.24", features = ["global-contracts"] }
  near-contract-standards = "5.24"
  [dev-dependencies]
  cargo-near-build = "0.11.1"
  near-sandbox = "0.3.5"
  near-api = { version = "0.8", default-features = false }
  tokio = { version = "1.44.0", features = ["full"] }
  walrus = "0.23.3"     # wasm rewriting, used by the upgrade tests
  testresult = "0.4"
  [package.metadata.near.reproducible_build]
  image = "sourcescan/cargo-near:0.19.0-rust-1.86.0"
  image_digest = "sha256:772638e343baeeea24e49062c7d424274f3441452cc06ce97fc4e5695b19fecc"
  container_build_command = ["cargo","near","build","non-reproducible-wasm","--locked"]
  ```
  (`sputnikdao-factory2` is identical; `sputnik-staking` and `test-token` use plain
  `near-sdk = "5.24"`.)
- CI `.github/workflows/tests.yml`: `dtolnay/rust-toolchain@1.86` with
  `target: wasm32-unknown-unknown`, then the cargo-near installer script pointed at
  `releases/latest` (**unpinned**).

### Build & test commands

```bash
# build.sh
(cd sputnik-staking     && cargo near build reproducible-wasm)
(cd sputnikdao2         && cargo near build reproducible-wasm)
(cd sputnikdao-factory2 && cargo near build reproducible-wasm)
(cd test-token          && cargo near build reproducible-wasm)

# CI test step (authoritative)
cargo test --workspace -- --nocapture
```
README prerequisites: NEAR account, near CLI, **cargo-near CLI**, and **Docker (for production
builds)**; also documents `cd sputnikdao-factory2 && cargo near build reproducible-wasm`.

### Local sandbox - and mainnet-state reuse (the standout)

`near-sandbox 0.3.5` plus `near-api 0.8` drive a real local `neard` sandbox **and import live
mainnet accounts**. `sputnikdao2/tests/utils/mod.rs:109-121`:
```rust
let sandbox = near_sandbox::Sandbox::start_sandbox().await?;
let sandbox_network =
    near_api::NetworkConfig::from_rpc_url("sandbox", sandbox.rpc_addr.parse()?);
sandbox
    .import_account(
        RPCEndpoint::mainnet().url,
        SPUTNIKDAO_FACTORY_CONTRACT_ACCOUNT.to_owned(),   // "sputnik-dao.near"
    )
    .initial_balance(NearToken::from_near(100))
    .send()
    .await?;
```
This is the NEAR analogue of `solana-test-validator --clone`: **the suite pulls `sputnik-dao.near`
state from mainnet RPC at test time.** Other sandbox entry points: `utils/mod.rs:172` (`setup_dao`),
`utils/mod.rs:654`, `tests/test_general.rs:970`, `sputnikdao-factory2/tests/test_upgrade.rs:42`,
`sputnikdao-factory2/tests/test_basics.rs:41`.

The factory setup also stores the DAO wasm on-chain and registers its code hash
(`store` -> `Base58CryptoHash` -> `set_default_code_hash`), which is the upgrade path a PoC would
target.

Committed binary fixtures (frozen deployed code, used by upgrade/compat tests):
```
sputnikdao2_original.wasm                    # previous deployed DAO version
sputnikdao2/tests/ref_exchange_release.wasm  # third-party (Ref Finance) contract
```

### Test/PoC conventions

`sputnikdao2/tests/`: `test_bounties.rs, test_delegation.rs, test_general.rs, test_proposals.rs,
test_skip_init.rs, test_upgrade.rs, test_views.rs, utils/mod.rs`.
`sputnikdao-factory2/tests/`: `test_basics.rs, test_upgrade.rs`.
Async `tokio` tests, `testresult::TestResult`, shared builders
`setup_dao() / setup_factory() / setup_dao_with_params()` returning
`TestContext { sandbox, sandbox_network, signer, root }`.

### Pitfalls

- `cargo test --workspace` **builds wasm inside the tests** (`cargo-near-build` is a
  dev-dependency), so the `wasm32-unknown-unknown` target is required.
- Tests hit **mainnet RPC** and download the `neard` sandbox binary -> strictly online; expect
  flakiness and rate-limits in an automated loop.
- `cargo near build reproducible-wasm` needs Docker plus the pinned sourcescan image.
- CI installs cargo-near `latest` (unpinned), which drifts from the 0.19.x image pinned in
  `[package.metadata.near.reproducible_build]`; pin it yourself for determinism.

---

## 5. sui - Move framework packages

`/workspace/scratch/clones/sui` (branch `main`, sparse)

### Toolchain pins

- `rust-toolchain.toml`:
  ```toml
  [toolchain]
  channel = "1.96.1"
  ```
- Root `Cargo.toml`: `[workspace] resolver = "2"` with a long `exclude` list pushing
  `external-crates/move/crates/*` and several examples out of the main workspace.
- Five Move packages under `crates/sui-framework/packages/`: `move-stdlib`, `sui-framework`,
  `sui-system`, `bridge`, `deepbook`.
  ```toml
  # packages/sui-framework/Move.toml
  [package] name = "Sui"; edition = "2024.beta"; published-at = "0x2"
  [dependencies] MoveStdlib = { local = "../move-stdlib" }
  [addresses] sui = "0x2"

  # packages/move-stdlib/Move.toml
  [package] name = "MoveStdlib"; edition = "2024.beta"; published-at = "0x1"
  [addresses] std = "0x1"

  # packages/sui-system/Move.toml
  [package] name = "SuiSystem"; version = "0.0.1"; published-at = "0x3"; edition = "2024.beta"
  [dependencies] MoveStdlib = { local = "../move-stdlib" }; Sui = { local = "../sui-framework" }
  [addresses] sui_system = "0x3"
  ```
  **All deps are `local =`; the framework packages have no git dependencies** and no
  `[dev-dependencies]` / `[dev-addresses]` sections.
- Move formatter pin, `.github/workflows/move-formatter.yml`: `FORMATTER_VERSION: 0.4.0`
  (`@mysten/prettier-plugin-move`), plus `cargo install tree-sitter-cli`.

### Build & test commands

The canonical framework test in CI is a **Rust** test, not the `sui` CLI
(`.github/workflows/rust.yml:497-499`, job `move-test`, gated on "Move changed but Rust did not"):
```yaml
- uses: taiki-e/install-action@nextest
- name: Run move tests
  run: cargo nextest run --profile ci --cargo-quiet -p sui-framework-tests --test move_tests
```
`crates/sui-framework-tests/tests/move_tests.rs` drives the Move unit tester in-process:
```rust
use move_cli::base::test::UnitTestResult;
use move_unit_test::UnitTestingConfig;
use sui_framework_tests::setup_examples;
use sui_move::unit_test::{MAX_UNIT_TEST_INSTRUCTIONS, run_move_unit_tests};
use sui_move_build::BuildConfig;
use sui_package_alt::SuiFlavor;
pub(crate) const EXAMPLES: &str = "../../examples";
pub(crate) const FRAMEWORK: &str = "../sui-framework/packages";
...
let mut config = BuildConfig::new_for_testing();
config.run_bytecode_verifier = true;
config.print_diags_to_stderr = true;
config.config.warnings_are_errors = true;
```
A `datatest_stable` harness walks every `Move.toml` under those roots, builds outside test mode,
then runs unit tests. Exclusions are explicit:
```rust
#[cfg(not(msim))] const DIRS_TO_EXCLUDE: &[&str] = &["oracle-adapter/move"];
#[cfg(msim)]      const DIRS_TO_EXCLUDE: &[&str] = &["nft-rental", "usdc_usage", "oracle-adapter/move"];
// "We cannot support packages that depend on git dependencies on simtests."
```
Per-package CLI equivalent (README step 5 references `run_framework_move_unit_tests`):
`sui move test --path crates/sui-framework/packages/sui-framework`.
Formatting: `npx prettier-move -c **/*.move` run per package directory.
Deterministic simulator suite: `scripts/simtest/cargo-simtest simtest --profile ci` with
`MSIM_TEST_SEED` derived from the commit SHA; nightlies in `simulator-nightly.yml`.

### Local sandbox / localnet

- **Move unit tests** - in-process, no node. 227 `.move` files under
  `crates/sui-framework/packages`, each package with a `tests/` dir, e.g.
  `packages/move-stdlib/tests/{vector,option,integer,u8,u32,u64,u128,ascii,bcs,hash,bit_vector,type_name,fixedpoint32,uq64_64,uq32_32}_tests.move`.
- **Localnet** - `sui start --force-regenesis --with-faucet`.
  `scripts/sui-test-validator.sh` is a deprecation shim that documents exactly this:
  ```
  sui-test-validator binary has been deprecated in favor of sui start ...
    * --with-faucet      --> start the faucet server on the default host and port
    * --force-regenesis  --> start without persisting state, from a new genesis
    * --with-indexer / --with-graphql (requires a local Postgres)
  ```
  `docs/content/references/cli/cheatsheet.mdx:162` lists `sui start`. The CI `move-test` job also
  puts Postgres on PATH for the wider Rust suite.
- **Ephemeral publishing**: `sui client test-publish` writes `Pub.localnet.toml` instead of
  polluting `Published.toml`
  (`docs/content/references/package-managers/package-manager-migration.mdx:85`).

### Mainnet-state reuse

No RPC state cloning in the framework tests, but three on-chain-truth artifacts:
- `crates/sui-framework/packages_compiled/{move-stdlib,sui-framework,sui-system,bridge,deepbook}` -
  committed compiled framework bytecode snapshots (21K-98K each), regenerated by
  `crates/sui-framework/tests/build-system-packages.rs`.
- `crates/sui-framework/published_api.txt` - 5969 lines of published API surface (for example
  "GenesisValidatorMetadata / public struct / 0x3::genesis"); an API-diff canary.
- `scripts/check-framework-compat.sh` diffs the repo framework against the **live chain**:
  ```bash
  # SUI env var, defaulting to the sui binary; REPO from git rev-parse --show-toplevel
  for PACKAGE in "$REPO"/crates/sui-framework/packages/*; do
      $SUI client verify-source "$PACKAGE"
  done
  ```
  (needs a `sui` binary plus a configured network -> online).

### Pitfalls

- The repo is enormous; a full checkout is multi-GB. Sparse checkout of
  `crates/sui-framework crates/sui-framework-tests crates/sui-move .github/workflows scripts` is
  30M and is sufficient for Move-level review.
- `-p sui-framework-tests` still compiles a large slice of the Rust workspace (move-compiler,
  move-package-alt-compilation, sui-move-build, sui-package-alt, sui-types, sui-config, tokio), so
  expect a long cold build even though the tests themselves are fast. `cargo nextest` is an extra
  install.
- `warnings_are_errors = true` in the harness: a warning anywhere in a dependency fails the run
  (see the `oracle-adapter/move` exclusion comment).
- Cheaper path: download the `sui` CLI release binary and run
  `sui move test --path crates/sui-framework/packages/<pkg>`, skipping the Rust workspace entirely.
  `sui` is **not** installed here.
- The `examples/` tree the harness walks has **git dependencies** (Pyth, Wormhole) -> not offline.
  The five framework packages alone are offline-capable (all `local =`).

---

## 6. aptos-core - Move framework

`/workspace/scratch/clones/aptos-core` (branch `mainnet`, sparse)

### Toolchain pins

- `rust-toolchain.toml`:
  ```toml
  [toolchain]
  channel = "1.94.1"
  # Note: we don't specify cargofmt in our toolchain because we rely on
  # the nightly version of cargofmt and verify formatting in CI/CD.
  components = ["cargo", "clippy", "rustc", "rust-docs", "rust-std"]
  ```
- Root `Cargo.toml`: `resolver = "2"`, **315 workspace members**, `rust-version = "1.88"`.
- Move packages under `aptos-move/framework/`: `move-stdlib`, `aptos-stdlib`, `aptos-framework`,
  `aptos-token`, `aptos-token-objects`, `aptos-trading`, `aptos-experimental`, plus
  `cached-packages`, `natives`, `table-natives`, `release-bundle`.
  ```toml
  # aptos-framework/Move.toml
  [package] name = "AptosFramework"; version = "1.0.0"
  [addresses] std="0x1"; aptos_std="0x1"; aptos_framework="0x1";
              aptos_fungible_asset="0xA"; aptos_token="0x3";
              core_resources="0xA550C18"; vm_reserved="0x0"
  [dependencies] AptosStdlib = { local="../aptos-stdlib" }; MoveStdlib = { local="../move-stdlib" }

  # aptos-stdlib/Move.toml
  [addresses] std="0x1"; aptos_std="0x1"; aptos_framework="0x1"; Extensions="0x1"
  [dependencies] MoveStdlib = { local="../move-stdlib" }

  # move-stdlib/Move.toml
  [package] name="MoveStdlib"; version="1.5.0"
  [addresses] vm="0x0"; std="0x1"
  ```
  All-local deps -> offline-friendly at the Move layer. 370 `.move` files under
  `aptos-move/framework`.
- Prover config `aptos-framework/Prover.toml`: `[backend] shards = 5`; the prover needs Boogie/Z3
  via `./.github/actions/move-prover-setup`.

### Build & test commands (`aptos-move/framework/README.md`)

```bash
cargo test                              # run inside aptos-move/framework
cargo test -p aptos-framework           # from anywhere
cargo test -- --skip prover             # skip the Move prover tests
cargo test -- aptos_stdlib --skip prover
TEST_FILTER="test_range_proof" cargo test -- aptos_stdlib --skip prover
REPORT_STATS=1 TEST_FILTER="bulletproofs" cargo test -- aptos_stdlib --skip prover
export RUST_MIN_STACK=4297152           # dev-mode stack overflows are common
cargo test --release -- --skip prover
aptos move document --help              # docgen through the CLI
```
Filter note from the README: "See tests in `tests/move_unit_test.rs` to determine which filter to
use; e.g., to run the tests in `aptos_framework` you must filter by `move_framework`."

CI:
```yaml
# coverage-move-only.yaml:53
cargo llvm-cov nextest --lcov --output-path lcov_unit.info --ignore-run-fail -p aptos-framework -p "move*"
# prover-daily-test.yaml:29-33
- uses: ./.github/actions/move-prover-setup
- run: MVP_TEST_DISALLOW_TIMEOUT_OVERWRITE=1 MVP_TEST_VC_TIMEOUT=7200 cargo test -p aptos-framework --release -- --include-ignored prover
- run: ... MVP_TEST_INCONSISTENCY=1 cargo test -p aptos-framework --release -- --include-ignored prover
```
Harness `aptos-move/framework/tests/move_unit_test.rs` (sibling `move_prover_tests.rs`):
```rust
use aptos_framework::{extended_checks, path_in_crate, BuildOptions};
use move_unit_test::{package_test::{run_move_unit_tests, UnitTestResult}, test_validation, UnitTestingConfig};
fn configure_extended_checks_for_unit_test() { test_validation::set_validation_hook(Box::new(validate)); }
let mut build_config = move_package::BuildConfig {
    test_mode: true, install_dir: Some(tempdir()...), full_model_generation: true, .. };
let utc = UnitTestingConfig {
    filter: std::env::var("TEST_FILTER").ok(),
    report_statistics: matches!(std::env::var("REPORT_STATS"), Ok(s) if s.as_str() == "1"), .. };
run_move_unit_tests(&pkg_path, build_config, utc, aptos_test_natives(),
                    aptos_test_feature_flags_genesis(), /* gas limit */ Some(100_000), ...)
```
Note the **extended checks** validation hook (aptos-specific attribute/lint checks run over test
code too) - worth mirroring in an automated workflow.
The `aptos move test` CLI path lives in `crates/aptos` (not in this sparse checkout) and is what a
bounty hunter would use on a standalone package; the framework itself is tested via this Rust
harness.

### Local sandbox / local testnet

- **Move unit tests** - fully in-process (`run_move_unit_tests` plus `aptos_test_natives()` and
  `aptos_test_feature_flags_genesis()`), no node.
- **Local testnet** - `aptos node run-local-testnet`, exercised by
  `.github/workflows/cli-e2e-tests.yaml`: "We run the CLI on this commit / PR against a local
  testnet using the devnet, testnet, and mainnet branches", driven by the python suite in
  `crates/aptos/e2e` (poetry 2.1.2).
- Framework Move tests at `aptos-move/framework/aptos-framework/tests/`:
  `account_abstraction_tests.move, aggregator_tests.move, aptos_coin_tests.move,
  clamped_token{,_tests}.move, deflation_token{,_tests}.move, delegation_pool_integration_tests.move,
  function_info_tests{,_helpers}.move, native_dispatch_token.move, native_disaptch_token_tests.move,
  nil_op_token{,_tests}.move, permissioned_signer_tests.move, permissioned_token{,_tests}.move,
  reentrant_token.move, confidential_asset/, datastructures/`
  - adversarial-token, reentrancy, and permissioned-signer fixtures already exist and are the
  natural place to drop a PoC module.

### Mainnet-state reuse (strongest of the six)

- `aptos-move/framework/cached-packages/src/head.mrb` - committed **Move release bundle**
  (compiled framework snapshot) shipped in-repo.
- `.github/workflows/module-verify.yaml` - "verify all modules that have been published on chain
  with the latest aptos node software", driven off public backup buckets:
  ```yaml
  BUCKET: aptos-mainnet-backup-backup-6addc21b     # testnet: aptos-testnet-backup-2223d95b
  SUB_DIR: e1
  BACKUP_CONFIG_TEMPLATE_PATH: terraform/helm/fullnode/files/backup/s3-public.yaml
  TIMEOUT_MINUTES: 720
  ```
- `.github/workflows/replay-verify-mainnet.yaml` - **replays real mainnet transactions**
  (`START_VERSION` / `END_VERSION` / `START_TIME` / `END_TIME` inputs) against the latest node
  build. A genuine mainnet-state differential harness, but it needs the public backup archive and
  hours of compute - not viable in a lightweight sandbox, though a good model if you have storage.

### Pitfalls

- 315-member workspace; `cargo test -p aptos-framework` still drags in `move-prover`,
  `move-prover-boogie-backend`, `move-prover-lab`, `move-model`, `move-compiler-v2`,
  `move-vm-runtime`, `aptos-vm`, `aptos-types` -> a very heavy cold build.
  **Always pass `-- --skip prover`** unless Boogie/Z3 are installed.
- `RUST_MIN_STACK=4297152` or `--release` is needed to avoid dev-mode stack overflows (documented).
- Rust 1.94.1 download required; `rust-version = 1.88` floor.
- Full checkout is multi-GB; sparse `aptos-move/framework .github/workflows scripts docker` = 17M.

---

## What an automated protocol-specific workflow can rely on

### klend (Solana / Anchor)
- **canonical build:** `solana-verify build` -> `target/deploy/kamino_lending.so` (CI job `build-klend`)
- **canonical test:** `cd libs/klend-interface && cargo test --lib && cargo test --test litesvm_integration`
- **sandbox entry point:** LiteSVM, in-process -
  `libs/klend-interface/tests/integration/setup.rs::setup_full_env()`
  (`LiteSVM::new().with_transaction_history(0)`, `svm.add_program(KLEND_PROGRAM_ID, &so_bytes)`)
- **blocks offline:** rustup 1.74.1 + 1.86.0 + nightly-2026-01-01; Solana SBF platform-tools or
  `cargo install --locked solana-verify`; two git `[patch.crates-io]` forks (pythnet-sdk,
  spl-token-2022) must be vendored; the `.so` is not committed, so a full SBF build gates the fast
  LiteSVM loop.

### near-core-contracts
- **canonical build:** `<contract>/build.sh`
  (`RUSTFLAGS='-C link-arg=-s' cargo build --target wasm32-unknown-unknown --release`), or
  `scripts/build_all.sh`
- **canonical test:** `scripts/test_all.sh` (per dir `RUSTFLAGS='-D warnings' cargo test`)
- **sandbox entry point:** near-sdk-sim `RuntimeStandalone` - `lockup/tests/spec.rs`
  (`init_simulator(Some(genesis_config))`, `deploy!`), wasm loaded from committed `res/*.wasm` via
  `lazy_static_include_bytes!`
- **blocks offline:** crates.io fetch plus `wasm32-unknown-unknown`; `state-manipulation` needs
  unpinned `nightly` and `workspaces::sandbox()` (downloads `neard`). Otherwise the most
  offline-friendly repo: near-sdk-sim tests need no wasm build. The real risk is the opposite -
  near-sdk 2.x/3.x may not build on a modern rustc.

### near-intents
- **canonical build:** `make` / `make CONTRACT[/VARIANT] [REPRODUCIBLE=1]` ->
  `cargo near build (non-)reproducible-wasm --locked --features=contract` into `res/`
- **canonical test:** `make test` (= `cargo test --all --all-features`); targeted:
  `cargo integration-tests <defuse|poa|escrow-swap>`
- **sandbox entry point:** `near-kit` sandbox - `crates/testing/sandbox/src/lib.rs`
  (`near_kit::sandbox::SandboxConfig::shared()`), consumed by `defuse-tests`
- **blocks offline:** Rust 1.97.1 / edition 2024; `cargo-near 0.22.0` binary install; reproducible
  builds need Docker plus `sourcescan/cargo-near:0.22.0-rust-1.97.1` (digest-pinned); `near-kit`
  downloads `neard`; even `make` invokes `cargo metadata`; tests require `res/*.wasm` to pre-exist.

### sputnik-dao
- **canonical build:** `./build.sh` = `cargo near build reproducible-wasm` per member
- **canonical test:** `cargo test --workspace -- --nocapture`
- **sandbox entry point:** `near_sandbox::Sandbox::start_sandbox()` plus
  `near_api::NetworkConfig::from_rpc_url("sandbox", ...)` - `sputnikdao2/tests/utils/mod.rs:109`
- **blocks offline:** hard blocker -
  `sandbox.import_account(RPCEndpoint::mainnet().url, "sputnik-dao.near")` hits mainnet RPC during
  tests; plus the `neard` binary download, cargo-near (installed unpinned in CI), Docker for
  reproducible-wasm, and `cargo-near-build` as a dev-dep meaning `cargo test` compiles wasm itself.

### sui
- **canonical build:** `cargo build -p sui-framework-tests`, or with the `sui` CLI
  `sui move build --path crates/sui-framework/packages/<pkg>`
- **canonical test:** `cargo nextest run --profile ci -p sui-framework-tests --test move_tests`;
  per package `sui move test --path crates/sui-framework/packages/<pkg>`
- **sandbox entry point:** Move unit tests in-process
  (`sui_move::unit_test::run_move_unit_tests`, `run_bytecode_verifier = true`,
  `warnings_are_errors = true`); node-level `sui start --force-regenesis --with-faucet`
- **blocks offline:** Rust 1.96.1 plus a big cold Rust build (or a `sui` release-binary download -
  nothing is installed here); `cargo nextest` install; the harness also walks `examples/`, which has
  git dependencies (Pyth/Wormhole). The five framework packages themselves are all `local =` and
  therefore offline-capable. `scripts/check-framework-compat.sh` (`sui client verify-source`)
  needs a live network.

### aptos-core
- **canonical build:** framework is source-only; `cargo build -p aptos-framework`
  (the shipped artifact is the committed `cached-packages/src/head.mrb`)
- **canonical test:** `cd aptos-move/framework && cargo test -- --skip prover`
  (or `cargo test -p aptos-framework -- --skip prover`); `TEST_FILTER=...`, `REPORT_STATS=1`
- **sandbox entry point:** Move unit tests in-process -
  `aptos-move/framework/tests/move_unit_test.rs` (`run_move_unit_tests` plus `aptos_test_natives()`,
  `aptos_test_feature_flags_genesis()`, and the `extended_checks` validation hook); node-level
  `aptos node run-local-testnet`
- **blocks offline:** Rust 1.94.1; a very heavy cold build (315-member workspace pulls
  move-prover/aptos-vm even for unit tests); `RUST_MIN_STACK=4297152` or `--release` to avoid stack
  overflow; the prover needs Boogie+Z3, so always `--skip prover`; the mainnet-state harnesses
  (`module-verify.yaml`, `replay-verify-mainnet.yaml`) need public backup buckets and up to 720 min.

### Cross-cutting recommendations

1. **Fastest inner loops, ranked by cost:** aptos `cargo test -- --skip prover` is comparable to sui
   `sui move test` (if the CLI binary is fetched), then klend LiteSVM (needs a one-time cached
   `.so`), then near-core-contracts `cargo test` (needs an old toolchain that still builds), and
   far behind near-intents / sputnik-dao (both need a `neard` sandbox binary; sputnik also needs
   mainnet RPC).
2. **Pre-bake a toolchain image.** Nothing is installed here, not even a default rustc. Carry:
   rustup toolchains 1.74.1 / 1.86 / 1.94.1 / 1.96.1 / 1.97.1 plus nightly, target
   `wasm32-unknown-unknown`, `cargo-nextest`, `cargo-near` 0.19.x and 0.22.0, Solana platform-tools
   (`cargo-build-sbf`), the `sui` CLI, the `aptos` CLI, and `neard`-sandbox binaries.
3. **Cache compiled artifacts, not source.** klend's `.so`, near-intents' `res/*.wasm`,
   near-core-contracts' `res/*.wasm`, sui's `packages_compiled/`, and aptos' `head.mrb` are the
   natural cache keys; three of five are already committed in-repo.
4. **Only sputnik-dao does live mainnet-account cloning**
   (`import_account(mainnet, "sputnik-dao.near")`); only aptos-core replays real chain history.
   Everything else synthesizes state, so protocol-specific PoCs must build fixtures by hand -
   klend's `setup.rs` is the reference pattern (hand-computed Anchor discriminators, hardcoded
   account sizes, a mock Pyth account, and `Clock` sysvar advancement).
5. **Drop-in PoC locations:** klend -> a new file in `libs/klend-interface/tests/integration/` plus
   a `mod.rs` entry; near legacy -> `<contract>/tests/*.rs`; near-intents ->
   `tests/src/tests/<subsystem>/`; sputnik -> `sputnikdao2/tests/` reusing `utils::setup_dao()`;
   sui -> `packages/<pkg>/tests/<name>_tests.move`; aptos ->
   `aptos-move/framework/aptos-framework/tests/<name>_tests.move`.
