import { beforeEach, describe, expect, it, vi } from "vitest";
import { exchange, parseRelayResponse, RelayProtocolError, RELAY_TIMEOUTS } from "./relay";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({ client: { exchangeComputerUse: vi.fn() } }));

const ok = (extra: Record<string, unknown> = {}) => JSON.stringify({ protocol: 2, active: true, available: true, reason: "", ...extra });

describe("parseRelayResponse", () => {
  it("accepts a v2 response with a request", () => {
    const response = parseRelayResponse(ok({ request: { id: "r1", action: { action: "left_click", coordinate: [512, 300] } } }));
    expect(response).toEqual({
      protocol: 2, active: true, available: true, reason: "",
      request: { id: "r1", action: { action: "left_click", coordinate: [512, 300] } },
    });
  });

  it("defaults a missing reason and ignores a null request", () => {
    expect(parseRelayResponse(JSON.stringify({ protocol: 2, active: false, available: false, request: null })))
      .toEqual({ protocol: 2, active: false, available: false, reason: "" });
  });

  it.each([
    ["not json", "{"],
    ["an array", "[]"],
    ["protocol v1", JSON.stringify({ protocol: 1, active: true, available: true })],
    ["missing protocol", JSON.stringify({ active: true, available: true })],
    ["non-boolean active", JSON.stringify({ protocol: 2, active: "yes", available: true })],
    ["non-string reason", ok({ reason: 3 })],
    ["empty request id", ok({ request: { id: "", action: { action: "screenshot" } } })],
    ["unknown action", ok({ request: { id: "r", action: { action: "rm_rf" } } })],
    ["wait action", ok({ request: { id: "r", action: { action: "wait", duration: 1 } } })],
    ["unknown field", ok({ request: { id: "r", action: { action: "screenshot", extra: 1 } } })],
    ["missing coordinate", ok({ request: { id: "r", action: { action: "left_click" } } })],
    ["negative coordinate", ok({ request: { id: "r", action: { action: "left_click", coordinate: [-1, 2] } } })],
    ["fractional coordinate", ok({ request: { id: "r", action: { action: "mouse_move", coordinate: [1.5, 2] } } })],
    ["inverted zoom region", ok({ request: { id: "r", action: { action: "zoom", region: [10, 10, 5, 20] } } })],
    ["bad scroll direction", ok({ request: { id: "r", action: { action: "scroll", scroll_direction: "sideways" } } })],
    ["empty text", ok({ request: { id: "r", action: { action: "type", text: "" } } })],
  ])("rejects %s", (_, raw) => {
    expect(() => parseRelayResponse(raw)).toThrow(RelayProtocolError);
  });

  it("explains a protocol mismatch", () => {
    expect(() => parseRelayResponse(JSON.stringify({ protocol: 1 }))).toThrow(/recreate the run/);
  });
});

describe("exchange", () => {
  beforeEach(() => vi.mocked(client.exchangeComputerUse).mockReset());

  it("sends identity, operation and timeout, and parses the response", async () => {
    vi.mocked(client.exchangeComputerUse).mockResolvedValue({ responseJson: ok() } as never);
    const response = await exchange({ namespace: "ns", name: "run" }, "sess", "next");
    expect(response.active).toBe(true);
    expect(client.exchangeComputerUse).toHaveBeenCalledWith(
      { namespace: "ns", name: "run", sessionId: "sess", operation: "next", requestId: "", outcomeJson: "" },
      { timeoutMs: 40_000, signal: undefined },
    );
    expect(RELAY_TIMEOUTS.connect).toBe(20_000);
  });

  it("serialises a result as outcomeJson and fills an empty failure error", async () => {
    vi.mocked(client.exchangeComputerUse).mockResolvedValue({ responseJson: ok() } as never);
    await exchange({ namespace: "ns", name: "run" }, "sess", "result", { result: { requestId: "r1", ok: false, error: "" } });
    const [request, options] = vi.mocked(client.exchangeComputerUse).mock.calls[0];
    expect(request).toMatchObject({ operation: "result", requestId: "r1" });
    expect(JSON.parse(request.outcomeJson as string)).toEqual({ requestId: "r1", ok: false, error: "Action failed" });
    expect(options?.timeoutMs).toBe(20_000);
  });

  it("sends only the strict Result keys with integer cursor coordinates", async () => {
    vi.mocked(client.exchangeComputerUse).mockResolvedValue({ responseJson: ok() } as never);
    const screenshot = { mediaType: "image/jpeg" as const, data: "QUJD", width: 10, height: 8, scale: 2 } as never;
    await exchange({ namespace: "ns", name: "run" }, "sess", "result", {
      result: { requestId: "r1", ok: true, error: "", denied: false, screenshot, cursor: { x: 1.4, y: 2.6, extra: 1 } as never },
    });
    expect(vi.mocked(client.exchangeComputerUse).mock.calls[0][0].outcomeJson).toBe(
      '{"requestId":"r1","ok":true,"screenshot":{"mediaType":"image/jpeg","data":"QUJD","width":10,"height":8},"cursor":{"x":1,"y":3}}',
    );
  });
});
