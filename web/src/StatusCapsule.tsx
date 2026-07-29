import { TriangleAlert } from "lucide-react";
import type { RunStatusPresentation } from "./runStatus";

/**
 * The one always-visible lifecycle readout on a run card: a colored dot, the
 * status label, and — when the observed state is uncertain — a warning icon.
 * Colour is never the only signal; markup mirrors .status-capsule in styles.css.
 */
export function StatusCapsule({ presentation }: { presentation: RunStatusPresentation }) {
  const tone = presentation.tone === "neutral" ? "" : presentation.tone;
  const className = ["status-capsule", tone, presentation.uncertain ? "uncertain" : ""].filter(Boolean).join(" ");
  return (
    <span className={className} data-status={presentation.lifecycle} title={presentation.detail || undefined}>
      <span className="status-capsule-dot" aria-hidden="true" />
      <span className="status-capsule-label">{presentation.label}</span>
      {presentation.uncertain ? <TriangleAlert size={11} strokeWidth={2.4} aria-hidden="true" /> : null}
    </span>
  );
}
