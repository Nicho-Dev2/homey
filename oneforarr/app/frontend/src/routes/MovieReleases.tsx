// Find releases: every result for one movie, with why each was accepted or
// rejected, and a button to send any of them.
//
// With ?replace=1 (the "Replace file…" link of a movie in the library) the
// releases are judged as if there were no file yet, so same-quality copies
// are offered, and sending one needs a second tap: it will replace the file.

import { useCallback, useEffect, useState } from "react";
import { errorText } from "../components/Form";
import { api, type Decision, type Movie } from "../lib/api";
import { formatAge, formatSize } from "../lib/format";
import { navigate, onLinkClick } from "../lib/router";

export function MovieReleases({ id }: { id: string }) {
  const [movie, setMovie] = useState<Movie | null>(null);
  const [decisions, setDecisions] = useState<Decision[] | null>(null);
  const [failures, setFailures] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [showRejected, setShowRejected] = useState(false);
  const replacing = new URLSearchParams(window.location.search).get("replace") === "1";

  const search = useCallback(async () => {
    setDecisions(null);
    setError("");
    try {
      const r = await api.post<{ decisions: Decision[]; failures: string[] }>(`/api/movies/${id}/releases${replacing ? "?replace=1" : ""}`);
      setDecisions(r.decisions);
      setFailures(r.failures);
    } catch (err) {
      setError(errorText(err));
    }
  }, [id, replacing]);

  useEffect(() => {
    api.get<{ movie: Movie }>(`/api/movies/${id}`).then((r) => setMovie(r.movie)).catch(() => {});
    search();
  }, [id, search]);

  const accepted = decisions?.filter((d) => d.accepted) ?? [];
  const rejected = decisions?.filter((d) => !d.accepted) ?? [];

  return (
    <>
      <header className="page-head">
        <h1>{replacing ? "Replace file" : "Releases"}{movie ? `: ${movie.title}` : ""}</h1>
        <a className="button" href={`/movies/${id}`} onClick={onLinkClick}>Back</a>
      </header>
      {replacing && movie?.file && (
        <p className="small">
          <strong>Replacing:</strong> {movie.file.quality} · {formatSize(movie.file.size)}.{" "}
          <span className="muted">Your current file stays until the new one is downloaded and in place.</span>
        </p>
      )}

      {error && <p className="error" role="alert">{error}</p>}
      {failures.map((f) => <p key={f} className="error small">{f}</p>)}
      {decisions === null && !error && <p className="muted">Searching every indexer…</p>}

      {decisions && (
        <>
          <p className="muted">{accepted.length} acceptable, {rejected.length} rejected.</p>
          <ul className="list">
            {accepted.map((d, i) => <ReleaseCard key={`a${i}`} id={id} d={d} best={i === 0} replacing={replacing} />)}
          </ul>
          {rejected.length > 0 && (
            <button type="button" className="button" onClick={() => setShowRejected(!showRejected)}>
              {showRejected ? "Hide" : "Show"} rejected ({rejected.length})
            </button>
          )}
          {showRejected && (
            <ul className="list rejected">
              {rejected.map((d, i) => <ReleaseCard key={`r${i}`} id={id} d={d} best={false} replacing={replacing} />)}
            </ul>
          )}
        </>
      )}
    </>
  );
}

function ReleaseCard({ id, d, best, replacing }: { id: string; d: Decision; best: boolean; replacing: boolean }) {
  const [state, setState] = useState<"idle" | "armed" | "busy" | "sent" | "error">("idle");
  const [message, setMessage] = useState("");
  const r = d.release;

  useEffect(() => {
    if (state !== "armed") return;
    const timer = window.setTimeout(() => setState("idle"), 4000);
    return () => window.clearTimeout(timer);
  }, [state]);

  async function grab() {
    if (replacing && state !== "armed") {
      setState("armed");
      return;
    }
    setState("busy");
    try {
      await api.post(`/api/movies/${id}/grab${replacing ? "?replace=1" : ""}`, r);
      setState("sent");
      setMessage("Sent to qBittorrent.");
      if (replacing) navigate(`/movies/${id}`);
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
          {state === "busy" ? "Sending…" : state === "sent" ? "Sent"
            : replacing ? (state === "armed" ? "Tap again to replace" : "Use this one")
            : d.accepted ? "Download" : "Download anyway"}
        </button>
        {message && <span className={state === "error" ? "error small" : "ok small"} role="status">{message}</span>}
      </div>
    </li>
  );
}
