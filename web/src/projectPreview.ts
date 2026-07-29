import type { ProjectRunCard } from "./types";

export const projectEvidencePreviewLimit = 2;

export function projectEvidencePreview(cards: ProjectRunCard[]): ProjectRunCard[] {
  return cards.slice(0, projectEvidencePreviewLimit);
}
