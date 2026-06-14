import { describe, expect, it } from "vitest";
import { makeT } from "./i18n";

describe("i18n", () => {
  it("returns locale-specific UI labels", () => {
    expect(makeT("zh")("runs")).toBe("实验");
    expect(makeT("en")("runs")).toBe("Runs");
  });
});
