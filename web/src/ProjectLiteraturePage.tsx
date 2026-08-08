import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { BookOpen, Check, ExternalLink, Library, LoaderCircle, Search, Send, ShieldCheck } from "lucide-react";
import {
  ApiError,
  createProjectJournalEntry,
  getProjectLiteratureCatalog,
  getProjectLiteratureStatus,
  queryProjectLiterature,
  saveProjectDefinition
} from "./api";
import type {
  LiteratureEvidence,
  LiteratureReference,
  Locale,
  ProjectDefinition
} from "./types";

function errorText(error: unknown) {
  if (error instanceof ApiError) return error.details || error.message;
  return error instanceof Error ? error.message : String(error);
}

function evidenceKey(evidence: LiteratureEvidence) {
  return `${evidence.zotero_item_key}:${evidence.chunk_sha256}`;
}

export function evidenceToReference(evidence: LiteratureEvidence, corpusRevision: string): LiteratureReference {
  return {
    source_kind: "frozen_corpus",
    zotero_item_key: evidence.zotero_item_key,
    zotero_uri: evidence.zotero_uri,
    page_label: evidence.page_label || (evidence.page != null ? String(evidence.page) : undefined),
    corpus_revision: corpusRevision,
    chunk_sha256: evidence.chunk_sha256
  };
}

export function literatureBindingAction(selectedKey: string, selectedProfile: string, currentKey: string, currentProfile: string, saving = false) {
  if (saving) return { disabled: true, state: "saving" as const };
  if (!selectedKey) return { disabled: true, state: "choose" as const };
  if (selectedKey === currentKey && !selectedProfile && !currentProfile) return { disabled: true, state: "needs_index" as const };
  if (selectedKey === currentKey && selectedProfile === currentProfile) return { disabled: true, state: "bound" as const };
  return { disabled: false, state: "save" as const };
}

export function ProjectLiteraturePage({
  token,
  locale,
  project,
  onOpenJournal
}: {
  token: string;
  locale: Locale;
  project: ProjectDefinition;
  onOpenJournal: (entryID: string) => void;
}) {
  const zh = locale === "zh";
  const queryClient = useQueryClient();
  const [collectionKey, setCollectionKey] = useState(project.zotero_collection_key || "");
  const [profileName, setProfileName] = useState(project.literature_service_profile || "");
  const [question, setQuestion] = useState("");
  const [selectedEvidence, setSelectedEvidence] = useState<Set<string>>(new Set());
  const [journalSaved, setJournalSaved] = useState("");
  const [searchElapsed, setSearchElapsed] = useState(0);

  useEffect(() => {
    setCollectionKey(project.zotero_collection_key || "");
    setProfileName(project.literature_service_profile || "");
  }, [project.id, project.zotero_collection_key, project.literature_service_profile]);

  const catalog = useQuery({
    queryKey: ["project-literature-catalog", token, project.id],
    queryFn: () => getProjectLiteratureCatalog(token, project.id),
    staleTime: 30_000,
    refetchOnWindowFocus: false
  });
  const status = useQuery({
    queryKey: ["project-literature-status", token, project.id, project.zotero_collection_key, project.literature_service_profile],
    queryFn: () => getProjectLiteratureStatus(token, project.id),
    refetchInterval: 30_000,
    refetchOnWindowFocus: "always"
  });

  const collections = catalog.data?.catalog.collections || [];
  const profiles = catalog.data?.catalog.profiles || [];
  const selectedCollection = collections.find((item) => item.key === collectionKey);
  const selectedProfile = profiles.find((item) => item.name === profileName);
  const persistedReadyProfile = profiles.find((item) =>
    item.name === project.literature_service_profile &&
    item.zotero_collection_key === project.zotero_collection_key &&
    item.status === "ready"
  );
  const queryReady = Boolean(persistedReadyProfile && status.data?.status === "ready");
  const bindingDirty = collectionKey !== (project.zotero_collection_key || "") || profileName !== (project.literature_service_profile || "");

  useEffect(() => {
    if (!collectionKey || profileName) return;
    const ready = profiles.find((item) => item.zotero_collection_key === collectionKey && item.status === "ready");
    if (ready) setProfileName(ready.name);
  }, [collectionKey, profileName, profiles]);

  const bind = useMutation({
    mutationFn: () => saveProjectDefinition(token, {
      ...project,
      zotero_collection_key: collectionKey,
      literature_service_profile: profileName
    }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["project-definitions", token] }),
        queryClient.invalidateQueries({ queryKey: ["project-literature-status", token, project.id] }),
        queryClient.invalidateQueries({ queryKey: ["project-literature-catalog", token, project.id] })
      ]);
    }
  });
  const bindingAction = literatureBindingAction(
    collectionKey,
    profileName,
    project.zotero_collection_key || "",
    project.literature_service_profile || "",
    bind.isPending
  );

  const ask = useMutation({
    mutationFn: () => queryProjectLiterature(token, project.id, question.trim()),
    onSuccess: (result) => {
      setSelectedEvidence(new Set((result.evidence || []).map(evidenceKey)));
      setJournalSaved("");
    }
  });

  useEffect(() => {
    if (!ask.isPending) {
      setSearchElapsed(0);
      return;
    }
    const startedAt = Date.now();
    const update = () => setSearchElapsed(Math.max(1, Math.floor((Date.now() - startedAt) / 1000)));
    update();
    const timer = window.setInterval(update, 1000);
    return () => window.clearInterval(timer);
  }, [ask.isPending]);

  const chosenEvidence = useMemo(() =>
    (ask.data?.evidence || []).filter((item) => selectedEvidence.has(evidenceKey(item))),
  [ask.data?.evidence, selectedEvidence]);

  const writeJournal = useMutation({
    mutationFn: () => {
      if (!ask.data) throw new Error("No literature answer to save");
      const sourceLines = chosenEvidence.map((item) => {
        const page = item.page_label || (item.page != null ? String(item.page) : "");
        return `- ${item.title || item.zotero_item_key}${page ? `（${zh ? "页" : "p. "}${page}）` : ""}`;
      });
      return createProjectJournalEntry(token, project.id, {
        actor: "human",
        title: `${zh ? "文献检索" : "Literature review"}：${question.trim()}`,
        body_md: [
          `> ${zh ? "这是固定语料上的背景文献依据，不替代实验结果。" : "Background literature from a pinned corpus; it does not replace experimental evidence."}`,
          "",
          `**${zh ? "问题" : "Question"}**：${question.trim()}`,
          "",
          ask.data.answer,
          "",
          `**${zh ? "固定语料" : "Pinned corpus"}**：\`${ask.data.corpus_revision}\``,
          "",
          `**${zh ? "引用" : "Sources"}**`,
          ...sourceLines
        ].join("\n"),
        literature_refs: chosenEvidence.map((item) => evidenceToReference(item, ask.data!.corpus_revision))
      });
    },
    onSuccess: async (entry) => {
      setJournalSaved(entry.id);
      await queryClient.invalidateQueries({ queryKey: ["project-journal", token, project.id] });
    }
  });

  function chooseCollection(nextKey: string) {
    setCollectionKey(nextKey);
    const ready = profiles.find((item) => item.zotero_collection_key === nextKey && item.status === "ready");
    setProfileName(ready?.name || "");
  }

  function submitQuestion() {
    if (queryReady && question.trim().length >= 3 && !ask.isPending) ask.mutate();
  }

  const quickQuestions = zh
    ? ["这些文献采用了哪些核心方法？", "哪些机制可以迁移到当前实验？", "现有证据还缺少哪些验证？"]
    : ["What core methods do these papers use?", "Which mechanisms transfer to the current experiment?", "What validation is still missing?"];

  return (
    <section className="project-literature-page">
      <header className="literature-heading">
        <div>
          <span className="panel-kicker">{zh ? "项目文献" : "Project literature"}</span>
          <h1>{zh ? "项目文献检索" : "Project literature search"}</h1>
          <p>{zh ? "选择一个 Zotero 文献文件夹，在这里检索并把引用写入工作日志。" : "Choose a Zotero folder, search it here, and write selected citations to the journal."}</p>
        </div>
        <Library aria-hidden="true" />
      </header>

      <section className="literature-binding" aria-label={zh ? "文献绑定" : "Literature binding"}>
        <div className="literature-binding-fields">
          <label>
            <span>{zh ? "Zotero 文献文件夹" : "Zotero literature folder"}</span>
            <select value={collectionKey} onChange={(event) => chooseCollection(event.target.value)} disabled={catalog.isPending}>
              <option value="">{catalog.isPending ? (zh ? "正在读取 Zotero…" : "Reading Zotero…") : (zh ? "选择文献文件夹" : "Choose a literature folder")}</option>
              {collections.map((collection) => <option key={collection.key} value={collection.key}>{collection.path}</option>)}
            </select>
          </label>
          <button className={bindingAction.state === "bound" ? "literature-bound-button" : "primary"} type="button" disabled={bindingAction.disabled || !bindingDirty} onClick={() => bind.mutate()}>
            {bindingAction.state === "saving" ? (zh ? "保存中…" : "Saving…")
              : bindingAction.state === "bound" ? <><Check size={15} />{zh ? "已关联" : "Connected"}</>
                : bindingAction.state === "needs_index" ? (zh ? "等待检索索引" : "Waiting for index")
                : bindingAction.state === "choose" ? (zh ? "先选择文件夹" : "Choose a folder")
                  : (zh ? "使用这个文件夹" : "Use this folder")}
          </button>
        </div>
        <div className={`literature-binding-state ${queryReady ? "ready" : "pending"}`}>
          {queryReady ? <ShieldCheck size={18} /> : <BookOpen size={18} />}
          <div>
            <strong>{queryReady ? (zh ? "可以检索" : "Ready to search") : selectedCollection ? (zh ? "已选择，但还不能检索" : "Selected, but not searchable yet") : (zh ? "请选择文献文件夹" : "Choose a literature folder")}</strong>
            <span>
              {queryReady
                ? `${persistedReadyProfile?.documents || 0} ${zh ? "篇文献已建立检索索引" : "papers indexed for search"}`
                : selectedCollection && !selectedProfile
                  ? (zh ? "可以先关联到项目；该文件夹还需要建立检索索引。" : "You can connect it to the project, but this folder still needs a search index.")
                  : status.data?.detail || (zh ? "系统会自动检查它是否可以检索。" : "ResearchOS will check whether it is searchable.")}
            </span>
            {queryReady ? <details><summary>{zh ? "技术信息" : "Technical details"}</summary><code>{persistedReadyProfile?.corpus_revision}</code></details> : null}
          </div>
        </div>
        {catalog.error || bind.error ? <div className="action-error">{errorText(catalog.error || bind.error)}</div> : null}
      </section>

      <form className="literature-query" onSubmit={(event) => { event.preventDefault(); submitQuestion(); }}>
        <Search size={19} />
        <textarea
          value={question}
          onChange={(event) => setQuestion(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              submitQuestion();
            }
          }}
          rows={2}
          placeholder={zh ? "这组文献对当前方法、模型选择或可迁移机制说明了什么？" : "What do these papers say about the method, model choice, or transferable mechanism?"}
        />
        <button className="primary" type="submit" disabled={!queryReady || question.trim().length < 3 || ask.isPending}>
          {ask.isPending ? <LoaderCircle className="spin" size={16} /> : <Send size={16} />}
          {ask.isPending ? (zh ? `检索中 ${searchElapsed}s` : `Searching ${searchElapsed}s`) : (zh ? "检索文献" : "Search literature")}
        </button>
      </form>
      <div className="literature-query-support">
        <p className="literature-query-hint">
          {!queryReady
            ? (zh ? "当前文件夹还不能在这里检索；Agent 仍可直接搜索 Zotero 全库。" : "This folder cannot be searched here yet; agents can still search the live Zotero library.")
            : ask.isPending
              ? (zh ? "正在检索文献并组织带页码的答案，通常需要 30–120 秒，复杂问题可能更久，请勿刷新。" : "Searching papers and composing a page-cited answer usually takes 30–120 seconds; complex questions can take longer. Please do not refresh.")
              : question.trim().length < 3
                ? (zh ? "输入至少 3 个字；点击“检索文献”或按 ⌘/Ctrl + Enter。" : "Enter at least 3 characters, then click Search or press Cmd/Ctrl + Enter.")
                : (zh ? "点击“检索文献”或按 ⌘/Ctrl + Enter。" : "Click Search or press Cmd/Ctrl + Enter.")}
        </p>
        {queryReady && !ask.isPending && !ask.data ? (
          <div className="literature-quick-questions" aria-label={zh ? "示例问题" : "Example questions"}>
            {quickQuestions.map((prompt) => <button key={prompt} type="button" onClick={() => setQuestion(prompt)}>{prompt}</button>)}
          </div>
        ) : null}
      </div>
      {ask.error ? <div className="action-error literature-query-error">{errorText(ask.error)}</div> : null}

      {ask.data ? (
        <div className="literature-result">
          <article className="literature-answer">
            <div className="literature-result-meta">
              <span>{ask.data.answerability || (zh ? "已回答" : "answered")}</span>
              <span>{zh ? "固定文献版本" : "pinned literature version"}</span>
              <span>{zh ? "仅作背景依据" : "background only"}</span>
            </div>
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{ask.data.answer || ""}</ReactMarkdown>
          </article>
          <aside className="literature-evidence">
            <header>
              <div>
                <span className="panel-kicker">{zh ? "定位证据" : "Located evidence"}</span>
                <strong>{chosenEvidence.length}/{ask.data.evidence?.length || 0} {zh ? "条将写入日志" : "selected for journal"}</strong>
              </div>
              <button
                className="primary"
                type="button"
                disabled={!chosenEvidence.length || writeJournal.isPending}
                onClick={() => writeJournal.mutate()}
              >
                <Check size={15} />
                {writeJournal.isPending ? (zh ? "写入中…" : "Writing…") : (zh ? "写入工作日志" : "Write to journal")}
              </button>
            </header>
            <div className="literature-evidence-list">
              {(ask.data.evidence || []).map((evidence) => {
                const key = evidenceKey(evidence);
                const checked = selectedEvidence.has(key);
                return (
                  <label className={checked ? "literature-evidence-row selected" : "literature-evidence-row"} key={key}>
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={() => setSelectedEvidence((current) => {
                        const next = new Set(current);
                        if (next.has(key)) next.delete(key); else next.add(key);
                        return next;
                      })}
                    />
                    <span>
                      <strong>{evidence.title || evidence.zotero_item_key}</strong>
                      <small>{evidence.page_label || evidence.page ? `${zh ? "页" : "p. "}${evidence.page_label || evidence.page}` : evidence.zotero_item_key}</small>
                      {evidence.text ? <em>{evidence.text}</em> : null}
                    </span>
                    <a href={evidence.zotero_uri} title={zh ? "在 Zotero 中打开" : "Open in Zotero"} onClick={(event) => event.stopPropagation()}>
                      <ExternalLink size={14} />
                    </a>
                  </label>
                );
              })}
            </div>
            {writeJournal.error ? <div className="action-error">{errorText(writeJournal.error)}</div> : null}
            {journalSaved ? (
              <button className="literature-journal-success" type="button" onClick={() => onOpenJournal(journalSaved)}>
                <Check size={15} />
                {zh ? "已固定引用，打开工作日志" : "Citations pinned — open journal"}
              </button>
            ) : null}
          </aside>
        </div>
      ) : ask.isPending ? (
        <div className="literature-empty literature-searching">
          <LoaderCircle className="spin" size={27} />
          <strong>{zh ? `正在检索 ${persistedReadyProfile?.documents || 0} 篇文献` : `Searching ${persistedReadyProfile?.documents || 0} papers`}</strong>
          <span>{zh ? `已等待 ${searchElapsed} 秒；结果会在这里直接展开。` : `${searchElapsed}s elapsed; results will appear here.`}</span>
        </div>
      ) : (
        <div className="literature-empty">
          <BookOpen size={25} />
          <strong>{queryReady ? (zh ? "输入一个研究问题开始检索" : "Enter a research question to search") : (zh ? "选择可检索的文献文件夹" : "Choose a searchable literature folder")}</strong>
          <span>{zh ? "系统会返回答案、具体论文和页码，确认后可写入工作日志。" : "ResearchOS returns an answer, papers, and pages that you can pin to the journal."}</span>
        </div>
      )}
    </section>
  );
}
