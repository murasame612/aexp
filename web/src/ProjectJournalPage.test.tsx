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
  });
});
