import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { JournalEntryRow } from "./ProjectJournalPage";
import type { ProjectJournalEntry } from "./types";

const entry: ProjectJournalEntry = {
  id: "journal_test",
  project_id: "project-a",
  actor: "agent",
  title: "Matched baseline is complete",
  body_md: "## Result\n\n| metric | value |\n|---|---|\n| mAP | 0.42 |",
  next_action: "Run five seeds",
  next_action_status: "open",
  run_ids: ["run_A"],
  literature_refs: [
    {
      source_kind: "frozen_corpus",
      zotero_item_key: "ITEMFROZEN",
      zotero_uri: "zotero://select/library/items/ITEMFROZEN",
      page_label: "12",
      corpus_revision: "corpus_abc",
      chunk_sha256: "sha256:1234567890abcdef1234567890abcdef"
    },
    {
      source_kind: "zotero_live",
      zotero_item_key: "ITEMLIVE",
      zotero_uri: "zotero://select/library/items/ITEMLIVE",
      item_version: 4,
      library_version: 88
    }
  ],
  created_at: "2026-07-27T08:00:00Z",
  updated_at: "2026-07-27T08:00:00Z"
};

describe("JournalEntryRow", () => {
  it("keeps the collapsed timeline compact", () => {
    const html = renderToStaticMarkup(
      <JournalEntryRow
        entry={entry}
        expanded={false}
        locale="en"
        runByID={new Map()}
        onToggle={() => undefined}
        onOpenRun={() => undefined}
        onToggleNextAction={() => undefined}
        nextActionBusy={false}
      />
    );
    expect(html).toContain('aria-expanded="false"');
    expect(html).toContain("Matched baseline is complete");
    expect(html).toContain('class="journal-entry-preview"');
    expect(html).toContain("2 references");
    expect(html).not.toContain("Frozen corpus");
    expect(html).not.toContain("<table>");
  });

  it("renders full Markdown and next action only when expanded", () => {
    const html = renderToStaticMarkup(
      <JournalEntryRow
        entry={entry}
        expanded
        locale="en"
        runByID={new Map()}
        onToggle={() => undefined}
        onOpenRun={() => undefined}
        onToggleNextAction={() => undefined}
        nextActionBusy={false}
      />
    );
    expect(html).toContain('aria-expanded="true"');
    expect(html).not.toContain('class="journal-entry-preview"');
    expect(html).toContain("<table>");
    expect(html).toContain("Run five seeds");
    expect(html).toContain("run_A");
    expect(html).toContain("Frozen corpus");
    expect(html).toContain("Zotero live");
    expect(html).toContain("corpus_abc");
    expect(html).toContain("item v4 · library v88");
    expect(html).toContain("zotero://select/library/items/ITEMLIVE");
  });
});
