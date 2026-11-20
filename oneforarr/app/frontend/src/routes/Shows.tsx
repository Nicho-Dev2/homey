// Shows: the library as a grid of posters, with episode counts.

import { useEffect, useMemo, useState } from "react";
import { errorText } from "../components/Form";
import { Badge, Poster, type Status } from "../components/MovieBits";
import { api, type ShowSummary } from "../lib/api";
import { onLinkClick } from "../lib/router";

type Filter = "all" | "missing" | "downloading" | "complete";

function showStatus(s: ShowSummary): Status {
  if (s.downloading > 0) return { label: `${s.have}/${s.episodes} ↓`, kind: "busy" };
  if (s.episodes > 0 && s.have >= s.episodes) return { label: `${s.have}/${s.episodes}`, kind: "done" };
  return { label: `${s.have}/${s.episodes}`, kind: "wanted" };
}

export function Shows() {
  const [shows, setShows] = useState<ShowSummary[] | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<Filter>("all");

  useEffect(() => {
    const load = () => api.get<ShowSummary[]>("/api/shows").then(setShows).catch((err) => setError(errorText(err)));
    load();
    const timer = window.setInterval(load, 15_000);
    return () => window.clearInterval(timer);
  }, []);

  const shown = useMemo(() => (shows ?? []).filter((s) => {
    if (filter === "missing") return s.have < s.episodes && s.downloading === 0;
    if (filter === "downloading") return s.downloading > 0;
    if (filter === "complete") return s.episodes > 0 && s.have >= s.episodes;
    return true;
  }), [shows, filter]);

  return (
    <>
      <header className="page-head">
        <h1>Shows</h1>
        <div className="button-row">
          <a className="button" href="/library" onClick={onLinkClick}>Scan folders</a>
          <a className="button primary" href="/shows/add" onClick={onLinkClick}>Add</a>
        </div>
      </header>

      {error && <p className="error" role="alert">{error}</p>}
      {shows === null && !error && <p className="muted">Loading…</p>}

      {shows?.length === 0 && (
        <div className="card empty">
          <p>No shows yet.</p>
          <a className="button primary" href="/shows/add" onClick={onLinkClick}>Add a show</a>
        </div>
      )}

      {shows && shows.length > 0 && (
        <div className="chips" role="group" aria-label="Show">
          {(["all", "missing", "downloading", "complete"] as Filter[]).map((f) => (
            <button key={f} type="button" className="chip" aria-pressed={filter === f} onClick={() => setFilter(f)}>
              {f[0].toUpperCase() + f.slice(1)}
            </button>
          ))}
        </div>
      )}

      <ul className="poster-grid">
        {shown.map((s) => (
          <li key={s.tmdbId}>
            <a href={`/shows/${s.tmdbId}`} onClick={onLinkClick} className="poster-card">
              <Poster path={s.posterPath} title={s.title} width={342} />
              <Badge status={showStatus(s)} />
              <span className="poster-title">{s.title}</span>
              <span className="muted small">{s.year || ""}{s.status === "Ended" ? " · Ended" : ""}</span>
            </a>
          </li>
        ))}
      </ul>
    </>
  );
}
