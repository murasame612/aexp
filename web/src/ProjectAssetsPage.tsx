import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Archive, ChevronLeft, ChevronRight, LoaderCircle } from "lucide-react";
import { getProjectAssets } from "./api";
import { fmtShortTime } from "./utils";

const pageSize = 50;

export function ProjectAssetsPage({ token, projectId }: { token: string; projectId: string }) {
  const [page, setPage] = useState(0);
  const assets = useQuery({
    queryKey: ["project-assets", token, projectId, page],
    queryFn: () => getProjectAssets(token, projectId, pageSize, page * pageSize)
  });
  const total = assets.data?.total || 0;
  const lastPage = Math.max(0, Math.ceil(total / pageSize) - 1);
  return <section className="project-assets-page">
    <header>
      <div><span className="panel-kicker">Assets</span><h2><Archive size={19}/> 已发布文件 revision</h2></div>
      <span>{total} assets</span>
    </header>
    <p className="muted">这里只显示该 Project 已校验的数据 revision 与 Run 发布输出。路径不是身份；Logical URI 与 revision 才是可复现引用。</p>
    {assets.isPending ? <div className="async-state"><LoaderCircle className="spin"/>加载 Assets…</div> : null}
    {assets.isError ? <div className="async-state error">{String(assets.error)}</div> : null}
    <div className="project-asset-list">
      {(assets.data?.items || []).map(asset=><article key={asset.id}>
        <div><b>{asset.role || "output"}</b><span>{fmtShortTime(asset.published_at)}</span></div>
        <code>{asset.logical_uri}</code>
        <code>{asset.revision}</code>
        <small>{asset.run_id ? `Run · ${asset.run_id}` : "Project Dataset Registry"}</small>
      </article>)}
      {!assets.isPending && !assets.data?.items.length ? <div className="muted">这个 Project 还没有已校验或已发布的 Asset。</div> : null}
    </div>
    {total > pageSize ? <footer><button disabled={page===0} onClick={()=>setPage(value=>Math.max(0,value-1))}><ChevronLeft size={15}/>上一页</button><span>{page+1}/{lastPage+1}</span><button disabled={page>=lastPage} onClick={()=>setPage(value=>value+1)}>下一页<ChevronRight size={15}/></button></footer> : null}
  </section>;
}
