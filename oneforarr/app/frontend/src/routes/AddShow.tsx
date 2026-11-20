// Add a show: search TMDB, pick one, choose quality and which episodes to
// follow.

import { useEffect, useState, type FormEvent } from "react";
import { errorText } from "../components/Form";
import { Poster } from "../components/MovieBits";
import { api, type MovieLookup, type Preset, type SettingsView } from "../lib/api";
import { navigate, onLinkClick } from "../lib/router";

const monitorOptions = [
  { id: "all", label: "All episodes" },
  { id: "future", label: "Future episodes only" },
  { id: "latest", label: "Latest season and new ones" },
  { id: "none", label: "Nothing (choose later)" },
];

export function AddShow() {
  const [text, setText] = useState("");
  const [results, setResults] = useState<MovieLookup[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [presets, setPresets] = useState<Preset[]>([]);
  const [defaultQuality, setDefaultQuality] = useState("1080p");
  const [open, setOpen] = useState<number | null>(null);

  useEffect(() => {
    api.get<Preset[]>("/api/movies/presets").then(setPresets).catch(() => {});
    api.get<SettingsView>("/api/settings").then((s) => setDefaultQuality(s.shows.quality)).catch(() => {});
  }, []);

  async function search(e: FormEvent) {
    e.preventDefault();
    if (!text.trim()) return;
    setBusy(true);
    setError("");
    try {
      setResults(await api.get<MovieLookup[]>(`/api/shows/lookup?q=${encodeURIComponent(text)}`));
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
        <h1>Add show</h1>
        <a className="button" href="/shows" onClick={onLinkClick}>Cancel</a>
      </header>

      <form className="card search-form" onSubmit={search}>
        <input type="search" placeholder="Title, e.g. The Twilight Zone" value={text} autoFocus
          onChange={(e) => setText(e.target.value)} aria-label="Show title" enterKeyHint="search" />
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
                <a className="button" href={`/shows/${r.tmdbId}`} onClick={onLinkClick}>In library</a>
              ) : open === r.tmdbId ? (
                <AddOptions id={r.tmdbId} presets={presets} defaultQuality={defaultQuality} />
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

function AddOptions({ id, presets, defaultQuality }: { id: number; presets: Preset[]; defaultQuality: string }) {
  const [quality, setQuality] = useState(defaultQuality);
  const [monitor, setMonitor] = useState("none"); // nothing followed until you choose
  const [searchNow, setSearchNow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function add() {
    setBusy(true);
    setError("");
    try {
      await api.post("/api/shows", { tmdbId: id, quality, monitor, search: searchNow });
      navigate(`/shows/${id}`);
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
      <label className="field">
        <span>Follow</span>
        <select value={monitor} onChange={(e) => setMonitor(e.target.value)}>
          {monitorOptions.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
        </select>
      </label>
      <label className="check">
        <input type="checkbox" checked={searchNow} onChange={(e) => setSearchNow(e.target.checked)} />
        Search for missing episodes now
      </label>
      {error && <p className="error small" role="alert">{error}</p>}
      <button type="button" className="button primary" onClick={add} disabled={busy}>{busy ? "Adding…" : "Add show"}</button>
    </div>
  );
}
