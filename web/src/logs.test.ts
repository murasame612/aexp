import { describe, expect, it } from "vitest";
import { isEmptyRemotePathSnapshot, logCursor, logSnapshotError, mergeCursorLogLines, mergeLogSnapshot } from "./logs";
import type { LogsResponse } from "./types";

function logs(payload: Partial<LogsResponse>): LogsResponse {
  return {
    run_id: "run_1",
    source: "events",
    total_lines: 0,
    offset: 0,
    limit: 5000,
    lines: [],
    ...payload
  };
}

describe("log helpers", () => {
  it("detects embedded snapshot read errors in 200 responses", () => {
    expect(logSnapshotError(logs({ error_kind: "remote_timeout", error: "deadline exceeded" }))).toBe("remote_timeout: deadline exceeded");
    expect(logSnapshotError(logs({ error: "log file not found" }))).toBe("log file not found");
    expect(logSnapshotError(logs({ lines: [{ content: "ok" }] }))).toBeNull();
    expect(logSnapshotError(logs({ error_kind: "resource_unreachable", error: "offline", lines: [{ content: "cached" }] }))).toBeNull();
  });

  it("detects suspicious empty remote path snapshots for retry", () => {
    expect(isEmptyRemotePathSnapshot(logs({ path: ".aexp/events/run.jsonl", remote: true, total_lines: 0, lines: [] }))).toBe(true);
    expect(isEmptyRemotePathSnapshot(logs({ path: ".aexp/events/run.jsonl", remote: true, total_lines: 2, lines: [] }))).toBe(false);
    expect(isEmptyRemotePathSnapshot(logs({ path: ".aexp/events/run.jsonl", remote: false, total_lines: 0, lines: [] }))).toBe(false);
    expect(isEmptyRemotePathSnapshot(logs({ path: ".aexp/events/run.jsonl", remote: true, total_lines: 0, error_kind: "remote_timeout" }))).toBe(false);
  });

  it("merges a successful snapshot without dropping live lines", () => {
    const merged = mergeLogSnapshot(
      [
        { source: "events", line_no: 1, content: "a" },
        { source: "events", line_no: 2, content: "b" }
      ],
      [
        { source: "events", line_no: 2, content: "b" },
        { source: "events", line_no: 3, content: "c" }
      ]
    );
    expect(merged.map((line) => line.content)).toEqual(["a", "b", "c"]);
  });

	it("fills cursor gaps without duplicating overlapping lines", () => {
		const current = [
			{ source: "stdout", line_no: 7, content: "same" },
			{ source: "stdout", line_no: 8, content: "old" }
		];
		const merged = mergeCursorLogLines(current, [
			{ source: "stdout", line_no: 8, content: "old" },
			{ source: "stdout", line_no: 9, content: "same" },
			{ source: "stdout", line_no: 10, content: "new" }
		]);
		expect(merged.map((line) => line.line_no)).toEqual([7, 8, 9, 10]);
		expect(logCursor(merged)).toBe(10);
	});

	it("replaces the previous generation after remote truncation", () => {
		const merged = mergeCursorLogLines(
			[{ source: "stdout", line_no: 99, content: "old file" }],
			[{ source: "stdout", line_no: 1, content: "new file" }],
			true
		);
		expect(merged).toEqual([{ source: "stdout", line_no: 1, content: "new file" }]);
	});
});
