// Discover: shelves of trending, new and popular movies and shows on the
// streaming services, like browsing a streaming app. Titles already
// downloaded are left out. Tap one to read about it and add it.

import { useEffect, useRef, useState } from "react";
import { errorText } from "../components/Form";
import { Poster } from "../components/MovieBits";
import { api, type DiscoverItem, type DiscoverPage, type Preset, type SettingsView } from "../lib/api";
import { formatAge, formatDate } from "../lib/format";
import { onLinkClick } from "../lib/router";

// The last page shown, so coming back to Discover (or reloading the browser)
// shows it at once. The server keeps the TMDB lists for 6 hours anyway; the
// quiet re-check on each visit only hides titles downloaded since.
let lastPage: DiscoverPage | null = readSaved();

function readSaved(): DiscoverPage | null {
  try {
    const raw = window.localStorage.getItem("discover");
    return raw ? (JSON.parse(raw) as DiscoverPage) : null;
  } catch {
    return null;
  }
}

function remember(p: DiscoverPage) {
  lastPage = p;
  try {
    window.localStorage.setItem("discover", JSON.stringify(p));
  } catch {
    // Storage full or off: it's only a shortcut.
  }
}

export function Discover() {
  const [page, setPage] = useState<DiscoverPage | null>(lastPage);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<DiscoverItem | null>(null);
  const [added, setAdded] = useState<Set<string>>(new Set());

  // refresh asks TMDB for new lists; otherwise the server's saved lists are
  // used, and nothing on screen changes while that quick check runs.
  async function load(refresh = false) {
    const shown = refresh || !lastPage;
    if (shown) setBusy(true);
    setError("");
    try {
      const p = await api.get<DiscoverPage>(`/api/discover${refresh ? "?refresh=1" : ""}`);
      remember(p);
      setPage(p);
    } catch (err) {
      if (shown) setError(errorText(err));
    } finally {
      if (shown) setBusy(false);
    }
  }
  useEffect(() => { load(); }, []);

  return (
    <>
      <header className="page-head">
        <h1>Discover</h1>
        <button type="button" className="button" onClick={() => load(true)} disabled={busy}>
          {busy ? "Loading…" : "Refresh"}
        </button>
      </header>

      {error && <p className="error" role="alert">{error}</p>}
      {!page && busy && <p className="muted">Gathering what's on the streaming services… the first time takes a few seconds.</p>}

      {page?.rows.map((row) => (
        <section key={row.id} className="shelf" aria-label={row.title}>
          <h2 className="shelf-title">
            {row.logoPath && <img className="shelf-logo" src={`https://image.tmdb.org/t/p/w92${row.logoPath}`} alt="" />}
            {row.title}
          </h2>
          <ul className="shelf-row">
            {row.items.map((it) => {
              const key = `${it.kind}:${it.tmdbId}`;
              return (
                <li key={key}>
                  <button type="button" className="shelf-card" onClick={() => setOpen(it)}>
                    <Poster path={it.posterPath} title={it.title} width={342} />
                    {(it.inApp || added.has(key)) && <span className="badge badge-wanted">In app</span>}
                    <span className="poster-title">{it.title}</span>
                    <span className="muted small">{it.kind === "show" ? "Show" : "Movie"}{it.kind === "movie" && it.releaseDate ? ` · ${formatDate(it.releaseDate)}` : it.year ? ` · ${it.year}` : ""}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      ))}

      {page && (
        <p className="muted small">
          {page.rows.length === 0 && "Nothing to show: everything here is already downloaded. "}
          Lists from <a href="https://www.themoviedb.org" target="_blank" rel="noreferrer">TMDB</a>; which service streams
          what comes from <a href="https://www.justwatch.com" target="_blank" rel="noreferrer">JustWatch</a>. Country: {page.region}{" "}
          (change it in <a href="/settings" onClick={onLinkClick}>Settings</a>). Updated {formatAge(page.updatedAt) === "now" ? "just now" : `${formatAge(page.updatedAt)} ago`}.
          {page.problems && page.problems.length > 0 && ` Some rows are missing: ${page.problems.join("; ")}`}
        </p>
      )}

      {open && (
        <Details item={open} added={added.has(`${open.kind}:${open.tmdbId}`)} onClose={() => setOpen(null)}
          onAdded={() => setAdded((s) => new Set(s).add(`${open.kind}:${open.tmdbId}`))} />
      )}
    </>
  );
}

const followOptions = [
  { id: "all", label: "All episodes" },
  { id: "future", label: "Future episodes only" },
  { id: "latest", label: "Latest season and new ones" },
  { id: "none", label: "Nothing (choose later)" },
];

// Details is the sheet that opens over the page for one title.
function Details({ item, added, onClose, onAdded }: { item: DiscoverItem; added: boolean; onClose: () => void; onAdded: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [quality, setQuality] = useState("");
  const [follow, setFollow] = useState("none"); // nothing followed until you choose
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState(added || item.inApp);
  const path = `/${item.kind === "show" ? "shows" : "movies"}`;

  useEffect(() => {
    dialog.current?.showModal();
    api.get<Preset[]>("/api/movies/presets").then(setPresets).catch(() => {});
    api.get<SettingsView>("/api/settings")
      .then((s) => setQuality(item.kind === "show" ? s.shows.quality : s.movies.quality))
      .catch(() => setQuality("1080p"));
  }, [item]);

  async function add() {
    setBusy(true);
    setError("");
    try {
      const body = item.kind === "show"
        ? { tmdbId: item.tmdbId, quality, monitor: follow, search: follow !== "none" }
        : { tmdbId: item.tmdbId, quality, search: true };
      await api.post(`/api${path}`, body);
      setDone(true);
      onAdded();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <dialog ref={dialog} className="sheet" onClose={onClose} onClick={(e) => { if (e.target === dialog.current) dialog.current?.close(); }}>
      <div className="sheet-body">
        {item.backdropPath && <img className="sheet-backdrop" src={`https://image.tmdb.org/t/p/w780${item.backdropPath}`} alt="" />}
        <div className="sheet-text">
          <h2>{item.title}</h2>
          <p className="muted small">
            {item.kind === "show" ? "Show" : "Movie"}
            {item.kind === "movie" && item.releaseDate ? ` · Released ${formatDate(item.releaseDate)}` : item.year ? ` · ${item.year}` : ""}
            {item.votes > 0 ? ` · ★ ${item.rating.toFixed(1)}` : ""}
          </p>
          <p>{item.overview || "No description on TMDB."}</p>

          {done ? (
            <a className="button primary" href={`${path}/${item.tmdbId}`} onClick={(e) => { dialog.current?.close(); onLinkClick(e); }}>
              Open {item.kind === "show" ? "show" : "movie"}
            </a>
          ) : (
            <div className="sheet-form">
              <label className="field">
                <span>Quality</span>
                <select value={quality} onChange={(e) => setQuality(e.target.value)}>
                  {presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
                </select>
              </label>
              {item.kind === "show" && (
                <label className="field">
                  <span>Follow</span>
                  <select value={follow} onChange={(e) => setFollow(e.target.value)}>
                    {followOptions.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
                  </select>
                </label>
              )}
              <button type="button" className="button primary" onClick={add} disabled={busy || !quality}>
                {busy ? "Adding…" : item.kind === "show" && follow === "none" ? "Add" : "Add and search"}
              </button>
            </div>
          )}
          {error && <p className="error" role="alert">{error}</p>}
          <button type="button" className="button wide" onClick={() => dialog.current?.close()}>Close</button>
        </div>
      </div>
    </dialog>
  );
}
