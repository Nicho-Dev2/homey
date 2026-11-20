// Library: scan the Movies and TV folders for files the app doesn't know,
// and pick the right movie or show for folders it couldn't match itself.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { errorText } from "../components/Form";
import { Poster } from "../components/MovieBits";
import { api, type LibraryView, type MovieLookup, type Unmatched } from "../lib/api";
import { formatAge } from "../lib/format";
import { onLinkClick } from "../lib/router";

export function Library() {
  const [lib, setLib] = useState<LibraryView | null>(null);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  const load = useCallback(() => {
    api.get<LibraryView>("/api/library").then(setLib).catch((err) => setError(errorText(err)));
  }, []);
  useEffect(load, [load]);

  // While a scan runs, check every 2 seconds.
  useEffect(() => {
    if (!lib?.scanning) return;
    const timer = window.setTimeout(load, 2000);
    return () => window.clearTimeout(timer);
  }, [lib, load]);

  async function scan() {
    setError("");
    setMessage("");
    try {
      setLib(await api.post<LibraryView>("/api/library/scan"));
    } catch (err) {
      setError(errorText(err));
    }
  }

  async function unskip(path: string) {
    try {
      setLib(await api.post<LibraryView>("/api/library/unskip", { path }));
    } catch (err) {
      setError(errorText(err));
    }
  }

  return (
    <>
      <header className="page-head">
        <h1>Library</h1>
        <button type="button" className="button primary" onClick={scan} disabled={!lib || lib.scanning}>
          {lib?.scanning ? "Scanning…" : "Scan folders"}
        </button>
      </header>

      <p className="muted">
        Reads the Movies and TV folders set in <a href="/settings" onClick={onLinkClick}>Settings</a>. Files
        that are gone are forgotten, and files dropped into a known movie's or show's folder are picked up.
        New folders are added when TMDB has exactly one match; shows added this way follow no episodes.
      </p>

      {error && <p className="error" role="alert">{error}</p>}
      {message && <p className="ok" role="status">{message}</p>}

      {lib && (
        <div className="card">
          {lib.scanning ? (
            <p>Scanning… this can take a minute with many new folders.</p>
          ) : lib.lastScan ? (
            <p><strong>Last scan {ago(lib.lastScan)}:</strong> {lib.report}</p>
          ) : (
            <p className="muted">Not scanned yet.</p>
          )}
        </div>
      )}

      {lib && lib.unmatched.length > 0 && (
        <>
          <h2>Needs a match</h2>
          <ul className="list">
            {lib.unmatched.map((u) => (
              <UnmatchedItem key={u.path} item={u} onDone={(next, msg) => { setLib(next); setMessage(msg); }} />
            ))}
          </ul>
        </>
      )}

      {lib && lib.skipped.length > 0 && (
        <details className="card">
          <summary>Skipped ({lib.skipped.length})</summary>
          <ul className="plain-list">
            {lib.skipped.map((p) => (
              <li key={p} className="row-between">
                <span className="small break">{p}</span>
                <button type="button" className="link-button" onClick={() => unskip(p)}>Undo</button>
              </li>
            ))}
          </ul>
        </details>
      )}
    </>
  );
}

function ago(iso: string): string {
  const age = formatAge(iso);
  return age === "now" ? "just now" : `${age} ago`;
}

function UnmatchedItem({ item, onDone }: { item: Unmatched; onDone: (lib: LibraryView, message: string) => void }) {
  const [text, setText] = useState(item.year ? `${item.title} ${item.year}` : item.title);
  const [results, setResults] = useState<MovieLookup[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const kind = item.kind === "movie" ? "movies" : "shows";

  async function search(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      setResults(await api.get<MovieLookup[]>(`/api/${kind}/lookup?q=${encodeURIComponent(text)}`));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function choose(r: MovieLookup) {
    setBusy(true);
    setError("");
    try {
      const res = await api.post<{ message: string; library: LibraryView }>("/api/library/match", { path: item.path, tmdbId: r.tmdbId });
      onDone(res.library, res.message);
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  }

  async function skip() {
    setBusy(true);
    try {
      onDone(await api.post<LibraryView>("/api/library/skip", { path: item.path }), "");
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <li className="card">
      <div className="row-between">
        <strong className="break">{item.name}</strong>
        <span className="muted small">{item.kind === "movie" ? "Movie" : "Show"}</span>
      </div>
      <p className="muted small">{item.note}</p>
      <form className="match-form" onSubmit={search}>
        <input type="search" value={text} onChange={(e) => setText(e.target.value)} aria-label={`Search TMDB for ${item.name}`} enterKeyHint="search" />
        <button className="button" disabled={busy}>Search</button>
        <button type="button" className="button" onClick={skip} disabled={busy}>Skip</button>
      </form>
      {error && <p className="error" role="alert">{error}</p>}
      {results?.length === 0 && <p className="muted">Nothing found on TMDB.</p>}
      {results && results.length > 0 && (
        <ul className="list">
          {results.slice(0, 6).map((r) => (
            <li key={r.tmdbId} className="lookup">
              <div className="lookup-poster"><Poster path={r.posterPath} title={r.title} width={185} /></div>
              <div className="lookup-body">
                <strong>{r.title}</strong> <span className="muted">{r.year || ""}</span>
                <p className="muted small clamp">{r.overview}</p>
                {r.inLibrary ? (
                  <span className="muted small">Already in the app</span>
                ) : (
                  <button type="button" className="button primary" onClick={() => choose(r)} disabled={busy}>This one</button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
