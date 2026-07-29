import type { Locale, Run } from "./types";
import { runObservationPresentation } from "./runStatus";
import { fmtShortTime } from "./utils";

export function RunObservationMeta({ run, locale, showError = true }: { run: Run; locale: Locale; showError?: boolean }) {
  const observation = runObservationPresentation(run, locale);
  return (
    <div className="run-observation-meta" title={showError ? observation.label : undefined}>
      <span>{locale === "zh" ? "新鲜度" : "freshness"} <strong>{observation.freshness}</strong></span>
      <span>{locale === "zh" ? "来源" : "source"} <strong>{observation.source}</strong></span>
      <span>{locale === "zh" ? "最后检查" : "last checked"} <strong>{observation.checked === "-" ? "-" : fmtShortTime(observation.checked)}</strong></span>
      {showError && observation.error ? <span className="observation-error">{observation.error}</span> : null}
    </div>
  );
}
