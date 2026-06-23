import { useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronLeft, ChevronRight, Clipboard, ExternalLink, Plus, Save, Trash2 } from "lucide-react";
import {
  createExperimentMatrix,
  deleteExperimentMatrix,
  getExperimentMatrices,
  getExperimentMatrix,
  getRuns,
  saveExperimentMatrixGrid,
  updateExperimentMatrix
} from "./api";
import type {
  ExperimentMatrix,
  ExperimentMatrixCell,
  ExperimentMatrixColumn,
  ExperimentMatrixDetail,
  ExperimentMatrixRow,
  Run
} from "./types";
import type { makeT } from "./i18n";
import { runTitle } from "./utils";

type T = ReturnType<typeof makeT>;

interface Props {
  token: string;
  t: T;
  onOpenRun: (id: string) => void;
}

export function ExperimentMatrixPage({ token, t, onOpenRun }: Props) {
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [metricKey, setMetricKey] = useState("");
  const [rows, setRows] = useState<ExperimentMatrixRow[]>([]);
  const [columns, setColumns] = useState<ExperimentMatrixColumn[]>([]);
  const [cells, setCells] = useState<ExperimentMatrixCell[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [titleMenuOpen, setTitleMenuOpen] = useState(false);
  const [copiedMarkdown, setCopiedMarkdown] = useState(false);

  const matrices = useQuery({ queryKey: ["experiment-matrices", token, query], queryFn: () => getExperimentMatrices(token, query), refetchInterval: 12000 });
  const selected = useQuery({ queryKey: ["experiment-matrix", token, selectedID], queryFn: () => getExperimentMatrix(token, selectedID), enabled: Boolean(selectedID) });
  const runOptions = useQuery({ queryKey: ["matrix-run-options", token], queryFn: () => getRuns(token, { limit: 200, offset: 0, refresh: false }), refetchInterval: 12000 });

  useEffect(() => {
    if (selectedID || !matrices.data?.length) return;
    setSelectedID(matrices.data[0].id);
  }, [matrices.data, selectedID]);

  useEffect(() => {
    const matrix = selected.data;
    if (!matrix) return;
    setTitle(matrix.title || "");
    setDescription(matrix.description || "");
    setMetricKey(matrix.default_metric_key || "");
    setRows(normalizeMatrixRows(matrix.rows || []));
    setColumns(normalizeMatrixColumns(matrix.columns || []));
    setCells(matrix.cells || []);
    setError("");
  }, [selected.data]);

  const matrixList = matrices.data || [];
  const selectedMatrix = selected.data;
  const runList = runOptions.data?.items || [];
  const cellBySlot = useMemo(() => {
    const out = new Map<string, ExperimentMatrixCell[]>();
    for (const cell of cells) {
      const key = slotKey(cell.row_id, cell.column_id);
      out.set(key, [...(out.get(key) || []), cell]);
    }
    return out;
  }, [cells]);

  async function createMatrix() {
    const next = await createExperimentMatrix(token, {
      title: title.trim() || t("newMatrix"),
      description,
      source_kind: "",
      source_id: "",
      source_name: "",
      default_metric_key: metricKey,
      seed_from_source: false
    });
    await queryClient.invalidateQueries({ queryKey: ["experiment-matrices"] });
    setSelectedID(next.id);
  }

  async function saveMatrix() {
    if (!selectedMatrix) return;
    setSaving(true);
    setError("");
    try {
      await updateExperimentMatrix(token, selectedMatrix.id, {
        title: title.trim() || selectedMatrix.title,
        description,
        source_kind: "",
        source_id: "",
        source_name: "",
        default_metric_key: metricKey,
        default_metric_goal: selectedMatrix.default_metric_goal || "",
        data_json: selectedMatrix.data_json || "{}"
      });
      const saved = await saveExperimentMatrixGrid(token, selectedMatrix.id, { rows, columns, cells });
      applyDetail(saved);
      await queryClient.invalidateQueries({ queryKey: ["experiment-matrices"] });
      await queryClient.invalidateQueries({ queryKey: ["experiment-matrix", token, selectedMatrix.id] });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }

  async function copyMarkdown() {
    if (!selectedMatrix) return;
    const markdown = buildMatrixMarkdown({
      title: title.trim() || selectedMatrix.title,
      description,
      rows,
      columns,
      cells
    });
    try {
      await navigator.clipboard.writeText(markdown);
      setCopiedMarkdown(true);
      window.setTimeout(() => setCopiedMarkdown(false), 1400);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function applyDetail(detail: ExperimentMatrixDetail) {
    setRows(detail.rows || []);
    setColumns(detail.columns || []);
    setCells(detail.cells || []);
  }

  async function removeMatrix(matrix: ExperimentMatrix) {
    await deleteExperimentMatrix(token, matrix.id);
    await queryClient.invalidateQueries({ queryKey: ["experiment-matrices"] });
    setSelectedID("");
  }

  function addRow() {
    setRows((prev) => [...prev, { id: makeID("mrow"), label: `实验 ${prev.length + 1}`, position: prev.length, data_json: "{}" }]);
  }

  function addColumn() {
    setColumns((prev) => [...prev, { id: makeID("mcol"), label: defaultMatrixColumn(prev.length), position: prev.length, data_json: "{}" }]);
  }

  return (
    <div className={sidebarCollapsed ? "matrix-page matrix-page-sidebar-collapsed" : "matrix-page"}>
      <aside className={sidebarCollapsed ? "matrix-sidebar collapsed" : "matrix-sidebar"}>
        <div className="matrix-sidebar-head">
          <div>
            <strong>{t("experimentMatrix")}</strong>
            <span>{matrixList.length} {t("matrices")}</span>
          </div>
          {!sidebarCollapsed ? (
            <div className="matrix-sidebar-controls">
              <button className="icon-button" type="button" title="收起列表" onClick={() => setSidebarCollapsed(true)}>
                <ChevronLeft size={16} />
              </button>
              <button className="icon-button" type="button" title={t("newMatrix")} onClick={createMatrix}>
                <Plus size={16} />
              </button>
            </div>
          ) : null}
        </div>
        {!sidebarCollapsed ? (
          <>
            <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} />
            <div className="matrix-list">
              {matrixList.map((matrix) => (
                <button key={matrix.id} className={matrix.id === selectedID ? "matrix-list-item active" : "matrix-list-item"} type="button" onClick={() => setSelectedID(matrix.id)}>
                  <strong>{matrix.title}</strong>
                  <span>{matrix.updated_at ? new Date(matrix.updated_at).toLocaleString() : t("experimentMatrix")}</span>
                </button>
              ))}
            </div>
          </>
        ) : null}
      </aside>

      <section className="matrix-main">
        {selectedMatrix ? (
          <>
            <div className="matrix-topbar">
              <div className="matrix-title-row">
                {sidebarCollapsed ? (
                  <button className="matrix-inline-list-button" type="button" title="打开矩阵列表" onClick={() => setSidebarCollapsed(false)}>
                    <ChevronRight size={15} />
                  </button>
                ) : null}
                <div className="matrix-title-picker">
                  <input className="matrix-title-input" value={title} placeholder={t("newMatrix")} onChange={(event) => setTitle(event.target.value)} />
                  <button
                    type="button"
                    className="matrix-title-menu-button"
                    aria-expanded={titleMenuOpen}
                    title="切换矩阵"
                    onClick={() => setTitleMenuOpen((value) => !value)}
                  >
                    <ChevronDown size={15} />
                  </button>
                  {titleMenuOpen ? (
                    <div className="matrix-title-menu">
                      {matrixList.map((matrix) => (
                        <button
                          key={matrix.id}
                          type="button"
                          className={matrix.id === selectedID ? "active" : ""}
                          onClick={() => {
                            setSelectedID(matrix.id);
                            setTitleMenuOpen(false);
                          }}
                        >
                          <strong>{matrix.title || t("newMatrix")}</strong>
                          <span>{matrix.updated_at ? new Date(matrix.updated_at).toLocaleString() : t("experimentMatrix")}</span>
                        </button>
                      ))}
                      <button type="button" className="matrix-title-menu-create" onClick={createMatrix}>
                        <Plus size={14} />
                        <strong>{t("newMatrix")}</strong>
                      </button>
                    </div>
                  ) : null}
                </div>
                <div className="matrix-actions">
                  <button type="button" onClick={addRow}>
                    <Plus size={15} />
                    {t("addRow")}
                  </button>
                  <button type="button" onClick={addColumn}>
                    <Plus size={15} />
                    {t("addColumn")}
                  </button>
                  <button type="button" className="primary" disabled={saving} onClick={saveMatrix}>
                    <Save size={15} />
                    {saving ? t("saving") : t("save")}
                  </button>
                  <button type="button" onClick={copyMarkdown}>
                    {copiedMarkdown ? <Check size={15} /> : <Clipboard size={15} />}
                    {copiedMarkdown ? "已复制" : "复制 MD"}
                  </button>
                  <button type="button" className="danger" onClick={() => removeMatrix(selectedMatrix)}>
                    <Trash2 size={15} />
                    {t("delete")}
                  </button>
                </div>
              </div>
              <textarea className="matrix-desc" value={description} onChange={(event) => setDescription(event.target.value)} placeholder={t("matrixDescriptionPlaceholder")} />
            </div>
            {error ? <div className="matrix-error">{error}</div> : null}
            <div className="matrix-source-line">
              <span>{rows.length} {t("matrixRows")}</span>
              <span>{columns.length} {t("matrixColumns")}</span>
              <span>{cells.length} {t("matrixCells")}</span>
            </div>
            <MatrixTable rows={rows} columns={columns} cellsBySlot={cellBySlot} setRows={setRows} setColumns={setColumns} setCells={setCells} runs={runList} onOpenRun={onOpenRun} onAddRow={addRow} onAddColumn={addColumn} t={t} />
          </>
        ) : (
          <div className="matrix-empty-state">{t("matrixNoSelection")}</div>
        )}
      </section>
    </div>
  );
}

function MatrixTable({
  rows,
  columns,
  cellsBySlot,
  setRows,
  setColumns,
  setCells,
  runs,
  onOpenRun,
  onAddRow,
  onAddColumn,
  t
}: {
  rows: ExperimentMatrixRow[];
  columns: ExperimentMatrixColumn[];
  cellsBySlot: Map<string, ExperimentMatrixCell[]>;
  setRows: Dispatch<SetStateAction<ExperimentMatrixRow[]>>;
  setColumns: Dispatch<SetStateAction<ExperimentMatrixColumn[]>>;
  setCells: Dispatch<SetStateAction<ExperimentMatrixCell[]>>;
  runs: Run[];
  onOpenRun: (id: string) => void;
  onAddRow: () => void;
  onAddColumn: () => void;
  t: T;
}) {
  const updateCell = (rowID: string, column: ExperimentMatrixColumn, value: string) => {
    setCells((prev) => {
      const existing = prev.find((cell) => cell.row_id === rowID && cell.column_id === column.id);
      const resolvedRunID = isRunIDColumn(column.label) ? resolveRunID(value, runs) : "";
      const next: ExperimentMatrixCell = {
        id: existing?.id || makeID("mcell"),
        row_id: rowID,
        column_id: column.id,
        title: "",
        statement: "",
        run_id: isRunIDColumn(column.label) ? resolvedRunID : existing?.run_id || "",
        project_card_id: existing?.project_card_id || "",
        metric_key: column.label,
        metric_value: isRunIDColumn(column.label) && resolvedRunID ? resolvedRunID : value,
        note: "",
        data_json: existing?.data_json || "{}"
      };
      return [...prev.filter((cell) => !(cell.row_id === rowID && cell.column_id === column.id)), next];
    });
  };

  return (
    <div className="matrix-table-shell">
      <table className="matrix-table">
        <thead>
          <tr>
            <th className="matrix-experiment-header">实验名称</th>
            {columns.map((column) => (
              <th key={column.id}>
                <textarea
                  className="matrix-col-head"
                  rows={1}
                  cols={4}
                  title={column.label}
                  value={column.label}
                  onChange={(event) => setColumns((prev) => prev.map((item) => (item.id === column.id ? { ...item, label: event.target.value.replace(/\n/g, "") } : item)))}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") event.preventDefault();
                  }}
                />
              </th>
            ))}
            <th className="matrix-add-col-head" title={t("addColumn")}>
              <button type="button" className="matrix-add-btn" onClick={onAddColumn} title={t("addColumn")}>
                <Plus size={15} />
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id}>
              <th className="matrix-experiment-cell">
                <MatrixTextCell value={row.label} onChange={(next) => setRows((prev) => prev.map((item) => (item.id === row.id ? { ...item, label: next } : item)))} />
              </th>
              {columns.map((column) => {
                const cell = cellsBySlot.get(slotKey(row.id, column.id))?.[0];
                const value = matrixCellValue(cell, column);
                return (
                  <td key={`${row.id}:${column.id}`}>
                    {isRunIDColumn(column.label) ? (
                      <RunPickerCell value={value} runs={runs} onChange={(next) => updateCell(row.id, column, next)} onOpenRun={onOpenRun} />
                    ) : (
                      <MatrixTextCell value={value} onChange={(next) => updateCell(row.id, column, next)} />
                    )}
                  </td>
                );
              })}
              <td className="matrix-add-col-cell" aria-hidden="true" />
            </tr>
          ))}
          <tr className="matrix-add-row">
            <th className="matrix-experiment-cell">
              <button type="button" className="matrix-add-row-btn" onClick={onAddRow}>
                <Plus size={15} />
                {t("addRow")}
              </button>
            </th>
            <td colSpan={columns.length + 1} aria-hidden="true" />
          </tr>
        </tbody>
      </table>
    </div>
  );
}

function MatrixTextCell({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  const resize = () => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${Math.max(46, textarea.scrollHeight)}px`;
  };

  useEffect(() => {
    resize();
  }, [value]);

  return (
    <textarea
      ref={textareaRef}
      value={value}
      onChange={(event) => {
        onChange(event.target.value);
        window.requestAnimationFrame(resize);
      }}
    />
  );
}

function RunPickerCell({ value, runs, onChange, onOpenRun }: { value: string; runs: Run[]; onChange: (value: string) => void; onOpenRun: (id: string) => void }) {
  const [draft, setDraft] = useState(value);
  const [focused, setFocused] = useState(false);
  useEffect(() => setDraft(value), [value]);

  const selectedRun = runs.find((run) => run.id === value);
  const normalized = draft.trim().toLowerCase();
  const matches = useMemo(() => {
    if (!normalized) return runs.slice(0, 8);
    return runs
      .filter((run) => `${run.id} ${runTitle(run)} ${run.kind || ""} ${run.status || ""}`.toLowerCase().includes(normalized))
      .slice(0, 8);
  }, [normalized, runs]);
  const openRunID = selectedRun?.id || (draft.trim().startsWith("run_") ? draft.trim() : "");
  const showMatches = matches.length > 0 && (focused || (!!normalized && !selectedRun));

  const chooseRun = (run: Run) => {
    setDraft(run.id);
    onChange(run.id);
    setFocused(false);
  };

  return (
    <div className="matrix-run-cell">
      <input
        value={draft}
        onFocus={() => setFocused(true)}
        onBlur={() => window.setTimeout(() => setFocused(false), 120)}
        onChange={(event) => {
          setDraft(event.target.value);
          onChange(event.target.value);
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter" && matches[0]) {
            event.preventDefault();
            chooseRun(matches[0]);
          }
        }}
        placeholder="匹配实验或 run_id"
        title={selectedRun ? `${runTitle(selectedRun)} · ${selectedRun.id}` : value}
      />
      {showMatches ? (
        <div className="matrix-run-matches">
          {matches.map((run) => (
            <button key={run.id} type="button" onMouseDown={(event) => event.preventDefault()} onClick={() => chooseRun(run)}>
              <strong>{runTitle(run)}</strong>
              <span>{run.id}</span>
            </button>
          ))}
        </div>
      ) : null}
      {openRunID ? (
        <button type="button" className="matrix-open-run" title="Open run" onClick={() => onOpenRun(openRunID)}>
          <ExternalLink size={13} />
        </button>
      ) : null}
    </div>
  );
}

function slotKey(rowID: string, columnID: string) {
  return `${rowID}\0${columnID}`;
}

function defaultMatrixColumn(index: number) {
  return ["run_id", "metric_1", "metric_2", "实验时间", "备注"][index] || `metric_${index}`;
}

function normalizeMatrixRows(rows: ExperimentMatrixRow[]) {
  if (!rows.length) return [{ id: makeID("mrow"), label: "实验 1", position: 0, data_json: "{}" }];
  return rows.map((row, index) => ({ ...row, label: /^行\s*\d+$/i.test(row.label.trim()) ? `实验 ${index + 1}` : row.label }));
}

function normalizeMatrixColumns(columns: ExperimentMatrixColumn[]) {
  const generic = columns.length <= 1 && (!columns[0]?.label || /^列\s*\d+$/i.test(columns[0].label.trim()) || columns[0].label.trim() === "结果");
  if (!columns.length || generic) {
    return ["run_id", "metric_1", "metric_2", "实验时间"].map((label, index) => ({ id: makeID("mcol"), label, position: index, data_json: "{}" }));
  }
  return columns;
}

function isRunIDColumn(label: string) {
  return label.trim().toLowerCase().replace(/\s+/g, "_") === "run_id";
}

function resolveRunID(value: string, runs: Run[]) {
  const normalized = value.trim();
  if (!normalized) return "";
  const lower = normalized.toLowerCase();
  const exact = runs.find((run) => run.id.toLowerCase() === lower || runTitle(run).toLowerCase() === lower);
  if (exact) return exact.id;
  return normalized.startsWith("run_") ? normalized : "";
}

function matrixCellValue(cell: ExperimentMatrixCell | undefined, column: ExperimentMatrixColumn) {
  if (!cell) return "";
  if (isRunIDColumn(column.label)) return cell.metric_value || cell.run_id || "";
  return cell.metric_value || cell.statement || cell.title || cell.note || "";
}

function buildMatrixMarkdown({
  title,
  description,
  rows,
  columns,
  cells
}: {
  title: string;
  description: string;
  rows: ExperimentMatrixRow[];
  columns: ExperimentMatrixColumn[];
  cells: ExperimentMatrixCell[];
}) {
  const headers = ["实验名称", ...columns.map((column) => column.label || "未命名列")];
  const lines = [`# ${title || "实验矩阵"}`];
  if (description.trim()) lines.push("", description.trim());
  lines.push("", `> ${rows.length} 行 · ${columns.length} 列 · ${cells.length} 格子`, "");
  lines.push(`| ${headers.map(escapeMarkdownCell).join(" | ")} |`);
  lines.push(`| ${headers.map(() => "---").join(" | ")} |`);
  for (const row of rows) {
    const values = columns.map((column) => {
      const slotCells = cells.filter((cell) => cell.row_id === row.id && cell.column_id === column.id);
      return slotCells.map((cell) => matrixCellValue(cell, column)).filter(Boolean).join("<br>");
    });
    lines.push(`| ${[row.label || "未命名实验", ...values].map(escapeMarkdownCell).join(" | ")} |`);
  }
  return `${lines.join("\n")}\n`;
}

function escapeMarkdownCell(value: string) {
  return String(value || "")
    .replace(/\r?\n/g, "<br>")
    .replace(/\|/g, "\\|")
    .trim();
}

function makeID(prefix: string) {
  return `${prefix}_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`;
}
