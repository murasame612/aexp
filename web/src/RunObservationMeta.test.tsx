import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Run } from "./types";
import { RunObservationMeta } from "./RunObservationMeta";

describe("RunObservationMeta", () => {
  it("renders freshness, source, checked time, and a probe error directly in a list row", () => {
    const run = {
      id: "run_unreachable",
      resource_id: "r",
      status: "running",
      command: "train",
      status_freshness: "stale",
      status_source: "local_cache",
      status_checked_at: "2026-07-15T00:00:00Z",
      status_check_error: "ssh timeout"
    } as Run;
    const html = renderToStaticMarkup(<RunObservationMeta run={run} locale="en" />);
    expect(html).toContain("freshness");
    expect(html).toContain("stale");
    expect(html).toContain("source");
    expect(html).toContain("local_cache");
    expect(html).toContain("last checked");
    expect(html).toContain("ssh timeout");
  });

  it("can leave the probe error to a separate warning block", () => {
    const run = {
      id: "run_unreachable",
      resource_id: "r",
      status: "running",
      command: "train",
      status_freshness: "stale",
      status_source: "local_cache",
      status_checked_at: "2026-07-15T00:00:00Z",
      status_check_error: "ssh timeout"
    } as Run;
    const html = renderToStaticMarkup(<RunObservationMeta run={run} locale="en" showError={false} />);
    expect(html).toContain("stale");
    expect(html).toContain("local_cache");
    expect(html).not.toContain("ssh timeout");
  });
});
