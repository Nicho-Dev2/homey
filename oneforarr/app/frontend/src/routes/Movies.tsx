// Movies: the library as a grid of posters.

import { useEffect, useMemo, useState } from "react";
import { errorText } from "../components/Form";
import { Badge, Poster, movieStatus } from "../components/MovieBits";
import { api, type Movie } from "../lib/api";
import { onLinkClick } from "../lib/router";

type Filter = "all" | "wanted" | "downloading" | "downloaded";

export function Movies() {
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<Filter>("all");

  useEffect(() => {
    const load = () => api.get<Movie[]>("/api/movies").then(setMovies).catch((err) => setError(errorText(err)));
    load();
    // Keep download progress fresh.
    const timer = window.setInterval(load, 10_000);
    return () => window.clearInterval(timer);
  }, []);

  const shown = useMemo(() => (movies ?? []).filter((m) => {
    if (filter === "wanted") return !m.file && !m.download;
    if (filter === "downloading") return !!m.download;
    if (filter === "downloaded") return !!m.file && !m.download;
    return true;
  }), [movies, filter]);

  return (
    <>
      <header className="page-head">
        <h1>Movies</h1>
        <div className="button-row">
          <a className="button" href="/library" onClick={onLinkClick}>Scan folders</a>
          <a className="button primary" href="/movies/add" onClick={onLinkClick}>Add</a>
        </div>
      </header>

      {error && <p className="error" role="alert">{error}</p>}
      {movies === null && !error && <p className="muted">Loading…</p>}

      {movies?.length === 0 && (
        <div className="card empty">
          <p>No movies yet.</p>
          <a className="button primary" href="/movies/add" onClick={onLinkClick}>Add a movie</a>
        </div>
      )}

      {movies && movies.length > 0 && (
        <div className="chips" role="group" aria-label="Show">
          {(["all", "wanted", "downloading", "downloaded"] as Filter[]).map((f) => (
            <button key={f} type="button" className="chip" aria-pressed={filter === f} onClick={() => setFilter(f)}>
              {f[0].toUpperCase() + f.slice(1)}
            </button>
          ))}
        </div>
      )}

      <ul className="poster-grid">
        {shown.map((m) => (
          <li key={m.tmdbId}>
            <a href={`/movies/${m.tmdbId}`} onClick={onLinkClick} className="poster-card">
              <Poster path={m.posterPath} title={m.title} width={342} />
              <Badge status={movieStatus(m)} />
              <span className="poster-title">{m.title}</span>
              <span className="muted small">{m.year || ""}</span>
            </a>
          </li>
        ))}
      </ul>
    </>
  );
}
