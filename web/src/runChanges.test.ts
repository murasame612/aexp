import { describe, expect, it } from "vitest";
import type { RunChangeItem, RunChangeResponse } from "./types";
import { catchUpRunChanges, createRunChangeSSEParser, readRunChangeStream, seedRunChangeCheckpoint } from "./runChanges";

describe("run change SSE parser", () => {
  it("seeds initial catch-up from the summary cursor instead of replaying history", () => {
    expect(seedRunChangeCheckpoint({ cursor: 0 }, 1026)).toEqual({ cursor: 1026 });
    expect(seedRunChangeCheckpoint({ cursor: 45, updatedSince: "now" }, 1026)).toEqual({ cursor: 45, updatedSince: "now" });
  });

  it("parses frames split across network chunks and advances the durable cursor", () => {
    const events: RunChangeItem[] = [];
    const parser = createRunChangeSSEParser((event) => events.push(event));
    parser.feed('id: 7\nevent: run-change\ndata: {"seq":7,"run_id":"run_new","operation":"up');
    parser.feed('sert","changed_at":"2026-07-15T00:00:00Z","run":{"id":"run_new","resource_id":"r","status":"queued"}}\n\n');
    expect(events).toHaveLength(1);
    expect(events[0].seq).toBe(7);
    expect(events[0].run?.id).toBe("run_new");
    expect(parser.cursor()).toBe(7);
  });

  it("ignores heartbeat comments and duplicate replay ids", () => {
    const events: RunChangeItem[] = [];
    const parser = createRunChangeSSEParser((event) => events.push(event), 9);
    parser.feed(': heartbeat\n\nid: 9\nevent: run-change\ndata: {"seq":9,"run_id":"old","operation":"upsert","changed_at":"x"}\n\n');
    expect(events).toEqual([]);
    expect(parser.cursor()).toBe(9);
  });

  it("streams an externally-created run with bearer auth and a durable cursor", async () => {
	const events: RunChangeItem[] = [];
	let requestedURL = "";
	let authorization = "";
	const fetchImpl = (async (input: RequestInfo | URL, init?: RequestInit) => {
		requestedURL = String(input);
		authorization = new Headers(init?.headers).get("Authorization") || "";
		return new Response('id: 12\nevent: run-change\ndata: {"seq":12,"run_id":"run_external","operation":"upsert","changed_at":"2026-07-15T00:00:00Z","run":{"id":"run_external","resource_id":"r","status":"queued"}}\n\n');
	}) as typeof fetch;
	const cursor = await readRunChangeStream("secret", 11, (change) => events.push(change), new AbortController().signal, fetchImpl);
	expect(requestedURL).toBe("/api/v1/runs/changes/stream?after_seq=11");
	expect(authorization).toBe("Bearer secret");
	expect(cursor).toBe(12);
	expect(events[0].run?.id).toBe("run_external");
  });

  it("uses updated_since for REST catch-up and filters already-applied changes", async () => {
	const response: RunChangeResponse = {
		items: [
			{ seq: 20, run_id: "old", operation: "upsert", changed_at: "2026-07-15T00:00:01Z" },
			{ seq: 21, run_id: "new", operation: "upsert", changed_at: "2026-07-15T00:00:02Z" }
		],
		next_seq: 21,
		server_time: "2026-07-15T00:00:03Z"
	};
	const calls: Array<[number, string | undefined]> = [];
	const result = await catchUpRunChanges({ cursor: 20, updatedSince: "2026-07-15T00:00:00Z" }, async (cursor, updatedSince) => {
		calls.push([cursor, updatedSince]);
		return response;
	});
	expect(calls).toEqual([[20, "2026-07-15T00:00:00Z"]]);
	expect(result.changes.map((change) => change.run_id)).toEqual(["new"]);
	expect(result.checkpoint).toEqual({ cursor: 21, updatedSince: "2026-07-15T00:00:03Z" });
  });

  it("paginates a full change page so quick terminal runs are not skipped", async () => {
    const first = Array.from({ length: 200 }, (_, index) => ({ seq: index + 1, run_id: `run_${index + 1}`, operation: "upsert", changed_at: "2026-07-15T00:00:00Z" }));
    const calls:number[]=[];
    const result=await catchUpRunChanges({cursor:0},async cursor=>{
      calls.push(cursor);
      return cursor===0?{items:first,next_seq:200,server_time:"2026-07-15T00:00:01Z"}:{items:[{seq:201,run_id:"run_quick_terminal",operation:"upsert",changed_at:"2026-07-15T00:00:01Z"}],next_seq:201,server_time:"2026-07-15T00:00:02Z"};
    });
    expect(calls).toEqual([0,200]);
    expect(result.changes.at(-1)?.run_id).toBe("run_quick_terminal");
    expect(result.checkpoint.cursor).toBe(201);
  });
});
