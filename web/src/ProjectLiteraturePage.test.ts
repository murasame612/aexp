import { describe, expect, it } from "vitest";
import { evidenceToReference, literatureBindingAction } from "./ProjectLiteraturePage";

describe("evidenceToReference", () => {
  it("pins Zotero identity, page, corpus revision, and chunk hash", () => {
    expect(evidenceToReference({
      zotero_item_key: "ITEM",
      zotero_uri: "zotero://select/library/items/ITEM",
      page_label: "12",
      chunk_sha256: "sha256:chunk"
    }, "corpus_revision")).toEqual({
      source_kind: "frozen_corpus",
      zotero_item_key: "ITEM",
      zotero_uri: "zotero://select/library/items/ITEM",
      page_label: "12",
      corpus_revision: "corpus_revision",
      chunk_sha256: "sha256:chunk"
    });
  });
});

describe("literatureBindingAction", () => {
  it("labels an unchanged binding as connected instead of showing a broken bind action", () => {
    expect(literatureBindingAction("COLL", "profile", "COLL", "profile")).toEqual({ disabled: true, state: "bound" });
  });

  it("allows a changed folder or automatically matched profile to be saved", () => {
    expect(literatureBindingAction("NEXT", "", "COLL", "profile")).toEqual({ disabled: false, state: "save" });
    expect(literatureBindingAction("COLL", "profile", "COLL", "")).toEqual({ disabled: false, state: "save" });
  });

  it("does not call a folder-only selection connected before it has an index profile", () => {
    expect(literatureBindingAction("COLL", "", "COLL", "")).toEqual({ disabled: true, state: "needs_index" });
  });
});
