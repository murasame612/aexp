import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { StatusCapsule } from "./StatusCapsule";
import type { RunStatusPresentation } from "./runStatus";

describe("StatusCapsule", () => {
  it("renders dot and label with the tone class", () => {
    const presentation: RunStatusPresentation = { lifecycle: "running", label: "running", tone: "good", uncertain: false, detail: "" };
    const html = renderToStaticMarkup(<StatusCapsule presentation={presentation} />);
    expect(html).toContain("status-capsule good");
    expect(html).toContain("status-capsule-dot");
    expect(html).toContain("status-capsule-label");
    expect(html).toContain("running");
    expect(html).not.toContain("uncertain");
    expect(html).not.toContain("<svg");
  });

  it("marks uncertain state with a class, icon, and detail tooltip", () => {
    const presentation: RunStatusPresentation = { lifecycle: "running", label: "status unknown", tone: "warn", uncertain: true, detail: "ssh timeout" };
    const html = renderToStaticMarkup(<StatusCapsule presentation={presentation} />);
    expect(html).toContain("status-capsule warn uncertain");
    expect(html).toContain("<svg");
    expect(html).toContain('title="ssh timeout"');
    expect(html).toContain("status unknown");
  });

  it("keeps neutral tone on the base styling", () => {
    const presentation: RunStatusPresentation = { lifecycle: "archived", label: "archived", tone: "neutral", uncertain: false, detail: "" };
    const html = renderToStaticMarkup(<StatusCapsule presentation={presentation} />);
    expect(html).toContain('class="status-capsule"');
  });
});
