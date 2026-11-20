// Add a movie: search TMDB, pick one, choose its quality.

import { useEffect, useState, type FormEvent } from "react";
import { errorText } from "../components/Form";
import { Poster } from "../components/MovieBits";
import { api, type Movie, type MovieLookup, type Preset, type SettingsView } from "../lib/api";
import { navigate, onLinkClick } from "../lib/router";

export function AddMovie() {
  const [text, setText] = useState("");
  const [results, setResults] = useState<MovieLookup[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [presets, setPresets] = useState<Preset[]>([]);
  const [defaultQuality, setDefaultQuality] = useState("1080p");
  const [open, setOpen] = useState<number | null>(null);

  useEffect(() => {
    api.get<Preset[]>("/api/movies/presets").then(setPresets).catch(() => {});
    api.get<SettingsView>("/api/settings").then((s) => setDefaultQuality(s.movies.quality)).catch(() => {});
  }, []);

  async function search(e: FormEvent) {
    e.preventDefault();
    if (!text.trim()) return;
    setBusy(true);
    setError("");
    try {
      setResults(await api.get<MovieLookup[]>(`/api/movies/lookup?q=${encodeURIComponent(text)}`));
      setOpen(null);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <header className="page-head">
        <h1>Add movie</h1>
        <a className="button" href="/movies" onClick={onLinkClick}>Cancel</a>
      </header>

      <form className="card search-form" onSubmit={search}>
        <input type="search" placeholder="Title, e.g. Night of the Living Dead 1968" value={text} autoFocus
          onChange={(e) => setText(e.target.value)} aria-label="Movie title" enterKeyHint="search" />
        <button className="button primary" disabled={busy}>{busy ? "Searching…" : "Search"}</button>
      </form>

      {error && <p className="error" role="alert">{error}</p>}
      {results?.length === 0 && <p className="muted">Nothing found on TMDB.</p>}

      <ul className="list">
        {results?.map((r) => (
          <li key={r.tmdbId} className="card lookup">
            <div className="lookup-poster"><Poster path={r.posterPath} title={r.title} width={185} /></div>
            <div className="lookup-body">
              <strong>{r.title}</strong> <span className="muted">{r.year || ""}</span>
              <p className="muted small clamp">{r.overview}</p>
              {r.inLibrary ? (
                <a className="button" href={`/movies/${r.tmdbId}`} onClick={onLinkClick}>In library</a>
              ) : open === r.tmdbId ? (
                <AddOptions movie={r} presets={presets} defaultQuality={defaultQuality} />
              ) : (
                <button type="button" className="button primary" onClick={() => setOpen(r.tmdbId)}>Add…</button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </>
  );
}

function AddOptions({ movie, presets, defaultQuality }: { movie: MovieLookup; presets: Preset[]; defaultQuality: string }) {
  const [quality, setQuality] = useState(defaultQuality);
  const [searchNow, setSearchNow] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function add() {
    setBusy(true);
    setError("");
    try {
      const m = await api.post<Movie>("/api/movies", { tmdbId: movie.tmdbId, quality, search: searchNow });
      navigate(`/movies/${m.tmdbId}`);
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <div className="add-options">
      <label className="field">
        <span>Quality</span>
        <select value={quality} onChange={(e) => setQuality(e.target.value)}>
          {presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
        </select>
      </label>
      <label className="check">
        <input type="checkbox" checked={searchNow} onChange={(e) => setSearchNow(e.target.checked)} />
        Search for it now
      </label>
      {error && <p className="error small" role="alert">{error}</p>}
      <button type="button" className="button primary" onClick={add} disabled={busy}>{busy ? "Adding…" : "Add movie"}</button>
    </div>
  );
}
