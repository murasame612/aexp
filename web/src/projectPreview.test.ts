import { describe, expect, it } from "vitest";
import { projectEvidencePreview, projectEvidencePreviewLimit } from "./projectPreview";
import type { ProjectRunCard } from "./types";

function card(id: string): ProjectRunCard {
  return { id, project_id: "project-a", run_id: `run-${id}` };
}

describe("projectEvidencePreview", () => {
  it("keeps project rows bounded even when a project has many runs", () => {
    const cards = Array.from({ length: 84 }, (_, index) => card(String(index)));

    const preview = projectEvidencePreview(cards);

    expect(preview).toHaveLength(projectEvidencePreviewLimit);
    expect(preview.map((item) => item.id)).toEqual(["0", "1"]);
    expect(cards).toHaveLength(84);
  });
});
