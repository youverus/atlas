import { useEffect, useState } from "react";
import { api, ApiError, type UsageHistory } from "../api";
import { formatTokens, UsageHeatmap } from "./UsageHeatmap";

const speedFormat = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 });

function useUsageHistory() {
  const [history, setHistory] = useState<UsageHistory | null>(null);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const request = new AbortController();
    void api.usageHistory(Intl.DateTimeFormat().resolvedOptions().timeZone, request.signal)
      .then((next) => {
        if (!request.signal.aborted) setHistory(next);
      })
      .catch((cause: unknown) => {
        if (request.signal.aborted) return;
        setError(cause instanceof ApiError && cause.status === 501
          ? "当前运行模式没有历史用量数据"
          : "读取用量统计失败，请重试");
      })
      .finally(() => {
        if (!request.signal.aborted) setLoading(false);
      });
    return () => request.abort();
  }, [revision]);

  const refresh = () => {
    setLoading(true);
    setError("");
    setRevision((previous) => previous + 1);
  };
  return { history, error, loading, refresh };
}

export function HomeUsage({ hidden }: { hidden: boolean }) {
  const { history } = useUsageHistory();
  if (!history) return null;
  return (
    <div className="chat__home-usage" data-hidden={hidden || undefined}>
      <UsageHeatmap usage={history.days} />
    </div>
  );
}

export function UsagePanel() {
  const { history, error, loading, refresh } = useUsageHistory();

  return (
    <div className="usage-panel" aria-busy={loading}>
      <div className="usage-panel__heading">
        <p className="field__hint">用量来自本机已保存的会话记录。</p>
        <button type="button" className="button" onClick={refresh} disabled={loading}>
          {loading ? "读取中…" : "刷新"}
        </button>
      </div>
      {error ? <div className="usage-panel__status" role="alert">{error}</div> : null}
      {loading && !history ? <div className="usage-panel__status" role="status">正在读取用量…</div> : null}
      {history ? (
        <>
          <UsageHeatmap usage={history.days} historyNavigation />
          <section className="usage-panel__card" aria-labelledby="usage-models">
            <h2 id="usage-models">模型用量 · 全部历史</h2>
            {history.models.length === 0 ? (
              <p className="field__hint">暂无模型用量记录</p>
            ) : (
              <table className="usage-panel__table" aria-labelledby="usage-models">
                <thead><tr><th scope="col">模型</th><th scope="col">Token 用量</th><th scope="col">平均速度</th></tr></thead>
                <tbody>
                  {history.models.map(({ provider, model, totalTokens, tokensPerSec }) => (
                    <tr key={JSON.stringify([provider, model])}>
                      <th scope="row">
                        <span>{model || "未知模型"}</span>
                        {provider ? <span className="usage-panel__provider">{provider}</span> : null}
                      </th>
                      <td>{formatTokens(totalTokens)}</td>
                      <td>{tokensPerSec === null ? "—" : `${speedFormat.format(tokensPerSec)} Token/s`}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </>
      ) : null}
    </div>
  );
}
