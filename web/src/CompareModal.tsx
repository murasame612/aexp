import { useQueries, useQuery } from "@tanstack/react-query";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { analyzeRunComparison, getLogs } from "./api";
import { parseEventLines } from "./events";
import type { I18nKey } from "./i18n";
import { logSnapshotError } from "./logs";
import { MetricChart } from "./MetricChart";
import { Modal } from "./Modal";
import type { Run } from "./types";
import { runTitle, uiEventsPath } from "./utils";

export function CompareModal({ t, token, runs, onClose }: { t: (key: I18nKey) => string; token: string; runs: Run[]; onClose: () => void }) {
  const analysis = useQuery({ queryKey: ["run-comparison-analysis", token, ...runs.map((run) => run.id).sort()], queryFn: () => analyzeRunComparison(token, runs.map((run) => run.id)), enabled: runs.length >= 2 });
  const queries = useQueries({
    queries: runs.map((run) => ({
      queryKey: ["compare-events", token, run.id, uiEventsPath(run)],
      queryFn: async () => {
        const logs = await getLogs(token, run.id, { path: uiEventsPath(run), limit: 5000, tail: true });
        const snapshotError = logSnapshotError(logs);
        if (snapshotError) throw new Error(snapshotError);
        return { run, parsed: parseEventLines(logs.lines.map((line) => line.content)) };
      },
      enabled: !!uiEventsPath(run)
    }))
  });
  const points = queries.flatMap((query) => (query.data?.parsed.metrics || []).map((point) => ({ ...point, series: query.data?.run.name || query.data?.run.id })));
  return (
    <Modal title={t("compare")} onClose={onClose} wide>
      {analysis.data ? (
        <section className={`comparison-audit ${analysis.data.claim_ready ? "ready" : "not-ready"}`}>
          <div className="comparison-audit-summary"><strong>{analysis.data.claim_ready ? "Claim-ready comparison" : "Comparison needs attention"}</strong><span>structure: {analysis.data.structurally_comparable ? "compatible" : "incompatible"} · claim gate: {analysis.data.claim_ready ? "pass" : "fail"}</span></div>
          {analysis.data.issues.length ? <ul>{analysis.data.issues.map((issue, index) => <li key={`${issue.field}-${index}`} className={issue.severity}><strong>{issue.field}</strong> {issue.message}</li>)}</ul> : null}
          {analysis.data.aggregates.length ? <div className="seed-aggregate-grid">{analysis.data.aggregates.map((row) => <div key={`${row.run_id}-${row.metric_key}`}><span>{row.run_id} · {row.metric_key}</span><strong>{formatMetric(row.mean)} ± {formatMetric(row.stddev)}</strong><em>n={row.count} · [{formatMetric(row.min)}, {formatMetric(row.max)}]</em></div>)}</div> : null}
          <details><summary>Automatic report</summary><ReactMarkdown remarkPlugins={[remarkGfm]}>{analysis.data.report_markdown}</ReactMarkdown></details>
        </section>
      ) : analysis.isPending ? <div className="async-state compact">Checking comparability and aggregating seeds…</div> : analysis.error ? <div className="async-state bad">{analysis.error instanceof Error ? analysis.error.message : String(analysis.error)}</div> : null}
      <MetricChart points={points} />
      <div className="metric-strip">{runs.map((run) => <div className="metric-tile" key={run.id}><span>{runTitle(run)}</span><strong>{run.status}</strong></div>)}</div>
    </Modal>
  );
}

function formatMetric(value: number) {
  if (!Number.isFinite(value)) return "-";
  if (Math.abs(value) >= 1000 || Math.abs(value) < 0.001 && value !== 0) return value.toExponential(3);
  return value.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
}
