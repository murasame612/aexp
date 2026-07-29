import type { ProjectJournalEntry } from "./types";

export function journalPreview(markdown: string, maxLength = 220): string {
  const plain = markdown
    .replace(/```[\s\S]*?```/g, " code ")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^#{1,6}\s+/gm, "")
    .replace(/^>\s?/gm, "")
    .replace(/^\s*[-:| ]{3,}\s*$/gm, "")
    .replace(/[*_~`|]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  if (plain.length <= maxLength) return plain;
  return `${plain.slice(0, Math.max(0, maxLength - 1)).trimEnd()}…`;
}

export function sortJournalNewestFirst(entries: ProjectJournalEntry[]): ProjectJournalEntry[] {
  return [...entries].sort((left, right) => {
    const timeDelta = Date.parse(right.created_at) - Date.parse(left.created_at);
    return Number.isFinite(timeDelta) && timeDelta !== 0 ? timeDelta : right.id.localeCompare(left.id);
  });
}

export function groupJournalByDate(entries: ProjectJournalEntry[], locale: string) {
  const formatter = new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en", {
    year: "numeric",
    month: "long",
    day: "numeric"
  });
  const groups: Array<{ key: string; label: string; entries: ProjectJournalEntry[] }> = [];
  for (const entry of sortJournalNewestFirst(entries)) {
    const date = new Date(entry.created_at);
    const validDate = Number.isFinite(date.getTime());
    const key = validDate
      ? `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`
      : "unknown";
    let group = groups.find((candidate) => candidate.key === key);
    if (!group) {
      group = { key, label: validDate ? formatter.format(date) : "Unknown date", entries: [] };
      groups.push(group);
    }
    group.entries.push(entry);
  }
  return groups;
}
