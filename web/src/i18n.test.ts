import { describe, expect, it } from "vitest";
import { makeT } from "./i18n";

describe("i18n", () => {
  it("returns locale-specific UI labels", () => {
    expect(makeT("zh")("runs")).toBe("实验");
    expect(makeT("en")("runs")).toBe("Runs");
    expect(makeT("zh")("launchpad")).toBe("项目设置");
    expect(makeT("en")("launchpad")).toBe("Project setup");
    expect(makeT("zh")("evidenceChains")).toBe("证据图");
    expect(makeT("en")("evidenceChains")).toBe("Evidence graphs");
  });
});
