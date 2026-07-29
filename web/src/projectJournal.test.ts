import { describe, expect, it } from "vitest";
import { groupJournalByDate, journalPreview, sortJournalNewestFirst } from "./projectJournal";
import type { ProjectJournalEntry } from "./types";

function entry(id: string, created_at: string, body_md = ""): ProjectJournalEntry {
  return {
    id,
    project_id: "project-a",
    actor: "agent",
    title: id,
    body_md,
    next_action_status: "none",
    run_ids: [],
    created_at,
    updated_at: created_at
  };
}

describe("project journal helpers", () => {
  it("makes a compact plain-text Markdown preview", () => {
    expect(journalPreview("## Result\n**Matched** [baseline](https://example.test)\n\n`mAP=0.4`"))
      .toBe("Result Matched baseline mAP=0.4");
  });

  it("sorts newest first and groups by local date", () => {
    const older = entry("journal-a", "2026-07-25T09:00:00Z");
    const newer = entry("journal-b", "2026-07-26T09:00:00Z");
    expect(sortJournalNewestFirst([older, newer]).map((item) => item.id)).toEqual(["journal-b", "journal-a"]);
    expect(groupJournalByDate([older, newer], "en").flatMap((group) => group.entries.map((item) => item.id)))
      .toEqual(["journal-b", "journal-a"]);
  });
});
