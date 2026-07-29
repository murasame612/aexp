import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DataCenterPage } from "./DataCenterPage";

function renderPage(locale: "zh" | "en") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(
    <QueryClientProvider client={queryClient}>
      <DataCenterPage token="test-token" locale={locale} resources={[]} />
    </QueryClientProvider>
  );
}

describe("DataCenterPage", () => {
  it("presents storage, snapshots, and background transfer as the default mental model", () => {
    const html = renderPage("zh");

    expect(html).toContain("大文件留在 NAS");
    expect(html).toContain("需要复现时再冻结");
    expect(html).toContain("后台传输");
    expect(html).not.toContain("权威 NAS");
    expect(html).not.toContain("权威副本");
    expect(html.indexOf("NAS 存储节点")).toBeLessThan(html.indexOf("冻结的数据版本"));
    expect(html.indexOf("冻结的数据版本")).toBeLessThan(html.indexOf("后台传输"));
    expect(html.indexOf("后台传输")).toBeLessThan(html.indexOf("高级：路径映射"));
  });

  it("keeps path mapping in a closed advanced section", () => {
    const html = renderPage("en");
    const advanced = html.match(/<details class="data-center-section data-center-advanced"[^>]*>/)?.[0] || "";

    expect(advanced).not.toContain("open");
    expect(html).toContain("Advanced: path mappings");
    expect(html).toContain("Primary storage");
    expect(html).not.toContain("NAS is authoritative");
  });
});
