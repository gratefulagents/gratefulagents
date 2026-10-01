import { afterEach, describe, expect, it } from "vitest";

import { beginDraftSend, persistDraft, readDraft, restoreDraftAfterFailedSend } from "./composerDraft";

const KEY = "draft:ns/run";

describe("composer draft persistence across a send", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("does not resurrect submitted text when the view unmounts mid-send", () => {
    persistDraft(KEY, "ship it");
    const submitted = beginDraftSend(KEY, "ship it");
    expect(readDraft(KEY)).toBe("");

    // Unmount flush still holding the submitted composer text.
    persistDraft(KEY, "ship it", submitted);
    expect(readDraft(KEY)).toBe("");

    // Newer text typed during the send is still saved.
    persistDraft(KEY, "ship it now", submitted);
    expect(readDraft(KEY)).toBe("ship it now");
  });

  it("restores the draft only when the send fails", () => {
    const submitted = beginDraftSend(KEY, "ship it");
    restoreDraftAfterFailedSend(submitted);
    expect(readDraft(KEY)).toBe("ship it");
  });

  it("does not clobber a newer draft when restoring after a failure", () => {
    const submitted = beginDraftSend(KEY, "ship it");
    persistDraft(KEY, "something else", submitted);
    restoreDraftAfterFailedSend(submitted);
    expect(readDraft(KEY)).toBe("something else");
  });
});
