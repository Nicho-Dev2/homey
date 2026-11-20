// Find releases for a season (packs) or one episode, with why each was
// accepted or rejected, and a button to send any of them.

import { useCallback, useEffect, useState } from "react";
import { errorText } from "../components/Form";
import { api, type Decision, type Show, type Target } from "../lib/api";
import { formatAge, formatSize } from "../lib/format";
import { onLinkClick } from "../lib/router";

export function ShowReleases({ id }: { id: string }) {
  const params = new URLSearchParams(window.location.search);
  const target: Target = { season: Number(params.get("season") ?? 1), episode: Number(params.get("episode") ?? 0) };
  const label = target.episode
    ? `S${String(target.season).padStart(2, "0")}E${String(target.episode).padStart(2, "0")}`
    : `Season ${target.season}`;

  const [title, setTitle] = useState("");
  const [decisions, setDecisions] = useState<Decision[] | null>(null);
  const [failures, setFailures] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [showRejected, setShowRejected] = useState(false);

  const search = useCallback(async () => {
    setDecisions(null);
    setError("");
    try {
      const r = await api.post<{ decisions: Decision[]; failures: string[] }>(`/api/shows/${id}/releases`, target);
      setDecisions(r.decisions);
      setFailures(r.failures);
    } catch (err) {
      setError(errorText(err));
    }
  }, [id, target.season, target.episode]);

  useEffect(() => {
    api.get<{ show: Show }>(`/api/shows/${id}`).then((r) => setTitle(r.show.title)).catch(() => {});
    search();
  }, [id, search]);

  const accepted = decisions?.filter((d) => d.accepted) ?? [];
  const rejected = decisions?.filter((d) => !d.accepted) ?? [];

  return (
    <>
      <header className="page-head">
        <h1>{title ? `${title}: ` : ""}{label}</h1>
        <a className="button" href={`/shows/${id}`} onClick={onLinkClick}>Back</a>
      </header>

      {target.episode > 0 && (
        <p className="muted small">
          Want the whole season in one download?{" "}
          <a href={`/shows/${id}/releases?season=${target.season}`} onClick={onLinkClick}>See Season {target.season} packs</a>
        </p>
      )}
      {error && <p className="error" role="alert">{error}</p>}
      {failures.map((f) => <p key={f} className="error small">{f}</p>)}
      {decisions === null && !error && <p className="muted">Searching every indexer…</p>}

      {decisions && (
        <>
          <p className="muted">{accepted.length} acceptable, {rejected.length} rejected.</p>
          <ul className="list">
            {accepted.map((d, i) => <ReleaseCard key={`a${i}`} id={id} target={target} d={d} best={i === 0} />)}
          </ul>
          {rejected.length > 0 && (
            <button type="button" className="button" onClick={() => setShowRejected(!showRejected)}>
              {showRejected ? "Hide" : "Show"} rejected ({rejected.length})
            </button>
          )}
          {showRejected && (
            <ul className="list rejected">
              {rejected.map((d, i) => <ReleaseCard key={`r${i}`} id={id} target={target} d={d} best={false} />)}
            </ul>
          )}
        </>
      )}
    </>
  );
}

function ReleaseCard({ id, target, d, best }: { id: string; target: Target; d: Decision; best: boolean }) {
  const [state, setState] = useState<"idle" | "busy" | "sent" | "error">("idle");
  const [message, setMessage] = useState("");
  const r = d.release;

  async function grab() {
    setState("busy");
    try {
      await api.post(`/api/shows/${id}/grab`, { ...target, release: r });
      setState("sent");
      setMessage("Sent to qBittorrent.");
    } catch (err) {
      setState("error");
      setMessage(errorText(err));
    }
  }

  return (
    <li className="card result">
      <div className="result-title">{best && <span className="badge badge-done">Best</span>} {r.title}</div>
      <div className="result-meta muted small">
        <span>{d.quality}</span>
        <span>{formatSize(r.size)}</span>
        <span className={r.seeders > 0 ? "ok" : ""}>▲ {r.seeders}</span>
        <span>▼ {r.leechers}</span>
        <span>{formatAge(r.publishedAt)}</span>
        <span>{r.indexerName}</span>
      </div>
      {d.reasons.length > 0 && <p className="error small">{d.reasons.join(" · ")}</p>}
      <div className="result-actions">
        <button type="button" className={d.accepted ? "button primary" : "button"} onClick={grab}
          disabled={state === "busy" || state === "sent"}>
          {state === "busy" ? "Sending…" : state === "sent" ? "Sent" : d.accepted ? "Download" : "Download anyway"}
        </button>
        {message && <span className={state === "error" ? "error small" : "ok small"} role="status">{message}</span>}
      </div>
    </li>
  );
}
