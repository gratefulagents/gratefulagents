import { describe, expect, it } from "vitest";

import { IMMUNEFI_TARGET_CATALOG } from "@/lib/immunefiTargetCatalog";

describe("IMMUNEFI_TARGET_CATALOG", () => {
  it("contains exactly the 20 approved unique targets", () => {
    expect(IMMUNEFI_TARGET_CATALOG.map((target) => target.displayName)).toEqual([
      "LayerZero",
      "Wormhole",
      "Hyperlane",
      "Spark ALM controller",
      "Chainlink CCIP",
      "Optimism",
      "zkSync Era",
      "Arbitrum token-bridge-contracts",
      "Ethena",
      "Sky DSS",
      "Olympus v3",
      "Axelar GMP SDK Solidity",
      "Sui",
      "Aptos",
      "Polkadot SDK",
      "THORChain thornode",
      "TON",
      "Euler v2",
      "SSV",
      "Osmosis",
    ]);
    expect(new Set(IMMUNEFI_TARGET_CATALOG.map((target) => target.name)).size).toBe(20);
    expect(new Set(IMMUNEFI_TARGET_CATALOG.map((target) => target.repoUrl)).size).toBe(20);
    expect(
      IMMUNEFI_TARGET_CATALOG.find((target) => target.displayName.startsWith("Arbitrum"))?.repoUrl,
    ).toBe("https://github.com/OffchainLabs/token-bridge-contracts");
    expect(IMMUNEFI_TARGET_CATALOG.every((target) => target.policyPackRef === "bug-bounty")).toBe(true);
    expect(IMMUNEFI_TARGET_CATALOG.every((target) => target.name.startsWith("immunefi-"))).toBe(true);
    expect(IMMUNEFI_TARGET_CATALOG.every((target) => target.securityProgramRef.startsWith("immunefi-"))).toBe(true);
  });
});
