import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  describeRpcError,
  httpStatusFromError,
  isAbortError,
  isRetryableReadError,
  isTransientRpcError,
} from "./rpc-errors";

describe("isTransientRpcError", () => {
  it("treats proxy junk statuses (HTTP 424 → unknown) as transient", () => {
    // connect-web maps unlisted HTTP statuses to Code.Unknown with "HTTP nnn".
    expect(isTransientRpcError(new ConnectError("HTTP 424", Code.Unknown))).toBe(true);
  });

  it("treats unavailable and deadline errors as transient", () => {
    expect(isTransientRpcError(new ConnectError("HTTP 503", Code.Unavailable))).toBe(true);
    expect(isTransientRpcError(new ConnectError("timed out", Code.DeadlineExceeded))).toBe(true);
    expect(isTransientRpcError(new ConnectError("slow down", Code.ResourceExhausted))).toBe(true);
  });

  it("treats raw fetch network failures as transient", () => {
    expect(isTransientRpcError(new TypeError("Failed to fetch"))).toBe(true);
    expect(isTransientRpcError(new TypeError("Load failed"))).toBe(true);
  });

  it("treats JSON parse failures from garbage bodies as transient", () => {
    // response.json() on an HTML error page rejects with a SyntaxError.
    expect(isTransientRpcError(new SyntaxError("Unexpected token '<', \"<html>\" is not valid JSON"))).toBe(true);
    expect(isTransientRpcError(new SyntaxError("Unexpected end of JSON input"))).toBe(true);
  });

  it("treats connect-web protocol strings as transient", () => {
    expect(isTransientRpcError(new Error("missing response body"))).toBe(true);
    expect(isTransientRpcError(new Error("missing EndStreamResponse"))).toBe(true);
  });

  it("does not treat definitive server answers as transient", () => {
    expect(isTransientRpcError(new ConnectError("name required", Code.InvalidArgument))).toBe(false);
    expect(isTransientRpcError(new ConnectError("nope", Code.PermissionDenied))).toBe(false);
    expect(isTransientRpcError(new ConnectError("not found", Code.NotFound))).toBe(false);
    expect(isTransientRpcError(new ConnectError("already exists", Code.AlreadyExists))).toBe(false);
    expect(isTransientRpcError(new ConnectError("token expired", Code.Unauthenticated))).toBe(false);
  });

  it("does not treat caller aborts as transient", () => {
    const abort = new Error("The operation was aborted");
    abort.name = "AbortError";
    expect(isTransientRpcError(abort)).toBe(false);
    expect(isTransientRpcError(new ConnectError("canceled", Code.Canceled))).toBe(false);
    expect(isAbortError(abort)).toBe(true);
  });
});

describe("isRetryableReadError", () => {
  it("additionally allows internal errors for idempotent reads", () => {
    expect(isRetryableReadError(new ConnectError("apiserver blip", Code.Internal))).toBe(true);
    expect(isRetryableReadError(new ConnectError("bad input", Code.InvalidArgument))).toBe(false);
  });
});

describe("httpStatusFromError", () => {
  it("extracts the status from connect-web fallback messages", () => {
    expect(httpStatusFromError(new ConnectError("HTTP 424", Code.Unknown))).toBe(424);
    expect(httpStatusFromError(new ConnectError("no status here", Code.Unknown))).toBeNull();
  });
});

describe("describeRpcError", () => {
  it("describes transient failures with the embedded HTTP status", () => {
    const message = describeRpcError(new ConnectError("HTTP 424", Code.Unknown), "load Projects");
    expect(message).toContain("load Projects");
    expect(message).toContain("HTTP 424");
    expect(message).toContain("temporarily unreachable");
  });

  it("turns raw JSON parse noise into a human message", () => {
    const message = describeRpcError(
      new SyntaxError('Unexpected token \'<\', "<html>" is not valid JSON'),
      "load Projects",
    );
    expect(message).not.toContain("Unexpected token");
    expect(message).toContain("temporarily unreachable");
  });

  it("passes through meaningful server errors", () => {
    const message = describeRpcError(new ConnectError("name is required", Code.InvalidArgument));
    expect(message).toBe("name is required");
  });

  it("describes expired sessions", () => {
    expect(describeRpcError(new ConnectError("bad token", Code.Unauthenticated))).toContain("sign in");
  });
});
