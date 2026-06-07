# Repository Instructions

## Code Navigation

- When reviewing architecture, tracing behavior, or answering how this code works, prefer `codegraph_explore` first to inspect relevant symbols and call paths.
- Use ordinary file reads or `rg` after codegraph when checking prose docs, non-indexed files, exact surrounding context, or repository-wide text patterns.
- For implementation changes, let codegraph identify the blast radius before editing shared executor, API, store, or monitor behavior.

## Experiment Semantics

- Never treat an experiment smoke test as a real result. Mark smoke checks separately from formal experiment runs in design notes, reports, and summaries.
