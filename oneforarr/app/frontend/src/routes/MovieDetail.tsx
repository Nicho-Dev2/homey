// One movie: its status, and what you can do with it.
//
// Still wanted: Search now, Find releases and the quality picker.
// In the library: Look for Upgrade (only when an upgrade is possible),
// Replace file…, and the wanted quality with a Change link.
// Always, next to the description: Remove From Library, or Cancel Download
// while something downloads.

import { useCallback, useEffect, useState } from "react";
import { errorText } from "../components/Form";
import { Badge, CancelDownload, Poster, ProgressBar, StuckNote, formatETA, movieStatus } from "../components/MovieBits";
import { api, type Message, type Movie, type Preset } from "../lib/api";
import { formatAge, formatSize } from "../lib/format";
import { navigate, onLinkClick } from "../lib/router";

type Note = { ok: boolean; text: string };

export function MovieDetail({ id }: { id: string }) {
  const [movie, setMovie] = useState<Movie | null>(null);
  const [folder, setFolder] = useState("");
  const [upgradable, setUpgradable] = useState(false);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [error, setError] = useState("");
  const [message, setMessage] = useState<Note | null>(null);
  const [busy, setBusy] = useState("");
  const [changingQuality, setChangingQuality] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [deleteFiles, setDeleteFiles] = useState(false);

  const load = useCallback(() => {
    api.get<{ movie: Movie; folder: string; upgradable: boolean }>(`/api/movies/${id}`)
      .then((r) => { setMovie(r.movie); setFolder(r.folder); setUpgradable(r.upgradable); })
      .catch((err) => setError(errorText(err)));
  }, [id]);

  useEffect(() => {
    load();
    api.get<Preset[]>("/api/movies/presets").then(setPresets).catch(() => {});
    // Refresh while something may change: a search after adding, or a download.
    const timer = window.setInterval(load, 5_000);
    return () => window.clearInterval(timer);
  }, [load]);

  if (error && !movie) return <p className="error">{error}</p>;
  if (!movie) return <p className="muted">Loading…</p>;

  // searchNow is "Search now" for a wanted movie and "Look for Upgrade" for
  // one in the library: the same search, which only accepts better releases
  // when there's already a file.
  async function searchNow() {
    setBusy("search");
    setMessage(null);
    try {
      const r = await api.post<Message>(`/api/movies/${id}/search`);
      setMessage({ ok: true, text: r.message });
    } catch (err) {
      setMessage({ ok: false, text: errorText(err) });
    } finally {
      setBusy("");
      load();
    }
  }

  async function changeQuality(quality: string) {
    try {
      setMovie(await api.put<Movie>(`/api/movies/${id}`, { quality }));
      setChangingQuality(false);
      load();
    } catch (err) {
      setMessage({ ok: false, text: errorText(err) });
    }
  }

  async function remove() {
    setBusy("remove");
    try {
      await api.del(`/api/movies/${id}`, { deleteFiles });
      navigate("/movies");
    } catch (err) {
      setMessage({ ok: false, text: errorText(err) });
      setBusy("");
    }
  }

  const status = movieStatus(movie);
  const d = movie.download;
  const file = movie.file;
  const wanted = presets.find((p) => p.id === movie.quality)?.label ?? movie.quality;
  const qualityPicker = (
    <select value={movie.quality} onChange={(e) => changeQuality(e.target.value)} aria-label="Quality">
      {presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
    </select>
  );
  const downloadLabel = !file ? "Downloading" : d?.replace ? "Replacing with" : "Upgrading to";
  const cancelled = (text: string, ok: boolean) => {
    const after = file ? " Your current file is kept." : " That release won't be picked again; the movie stays wanted.";
    setMessage({ text: ok ? text + after : text, ok });
    load();
  };

  return (
    <>
      <header className="page-head">
        <a className="button" href="/movies" onClick={onLinkClick}>← Movies</a>
      </header>

      <section className="movie-head">
        <div className="movie-poster"><Poster path={movie.posterPath} title={movie.title} width={342} /></div>
        <div className="movie-info">
          <h1>{movie.title} <span className="muted">{movie.year || ""}</span></h1>
          <p className="muted small">
            {movie.runtime ? `${movie.runtime} min · ` : ""}
            <a href={`https://www.themoviedb.org/movie/${movie.tmdbId}`} target="_blank" rel="noreferrer">TMDB</a>
            {movie.imdbId && <> · <a href={`https://www.imdb.com/title/${movie.imdbId}/`} target="_blank" rel="noreferrer">IMDb</a></>}
          </p>
          <Badge status={status} />
          {movie.overview && <p className="overview">{movie.overview}</p>}
          {d ? (
            <div className="remove-corner">
              <CancelDownload kind="movie" id={movie.tmdbId} tag={d.tag} onDone={cancelled} />
            </div>
          ) : (
            <div className="remove-corner">
              {!confirmRemove ? (
                <button type="button" className="button danger small-button" onClick={() => setConfirmRemove(true)}>Remove From Library</button>
              ) : (
                <div className="remove-confirm">
                  {file ? (
                    <>
                      <label className="check">
                        <input type="checkbox" checked={deleteFiles} onChange={(e) => setDeleteFiles(e.target.checked)} />
                        Also delete <code>{folder}</code> from the movies folder
                      </label>
                      <p className="muted small">The download in qBittorrent is left alone, so it can keep seeding.</p>
                    </>
                  ) : (
                    <p className="muted small">The app stops looking for it. Nothing is downloaded yet, so no files are touched.</p>
                  )}
                  <div className="button-row">
                    <button type="button" className="button small-button" onClick={() => setConfirmRemove(false)}>Keep it</button>
                    <button type="button" className="button danger small-button" onClick={remove} disabled={busy === "remove"}>Remove</button>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </section>

      <section className="card section">
        <h2>Status</h2>
        {file ? (
          <>
            <p className="small">
              <strong>In the library</strong> · {file.quality} · {formatSize(file.size)} · wanted {wanted}{" "}
              <button type="button" className="link-button small" onClick={() => setChangingQuality(!changingQuality)}>
                {changingQuality ? "Close" : "Change"}
              </button>
            </p>
            {changingQuality && (
              <div className="quality-change">
                {qualityPicker}
                <p className="muted small">A higher quality makes the app look for an upgrade (with upgrades on in Settings).</p>
              </div>
            )}
            <p className="muted small path">{file.path}</p>
          </>
        ) : !d ? (
          <p className="small">Not downloaded yet. It will go in <code>{folder}</code>.</p>
        ) : null}

        {d && (
          <div className="download-block">
            <p className="small"><strong>{downloadLabel}</strong> {d.releaseTitle} <span className="muted">from {d.indexerName}</span></p>
            <div className="dl-line">
              <ProgressBar value={d.progress} />
              <span className="muted small" title={d.size ? formatSize(d.size) : ""}>{Math.floor(d.progress * 100)}% {formatETA(d.eta)}</span>
            </div>
            {file && <p className="muted small">Your {file.quality} copy stays until the new one is in place.</p>}
            {d.error && <p className="error small">{d.error}</p>}
            <StuckNote d={d} />
          </div>
        )}

        {movie.lastSearch && <p className="muted small">{ago(movie.lastSearch.at)}: {movie.lastSearch.message}</p>}
        {message && <p className={message.ok ? "ok small" : "error small"} role="status">{message.text}</p>}

        {!file && !d && (
          <div className="button-row">
            <button type="button" className="button primary" onClick={searchNow} disabled={busy === "search"}>
              {busy === "search" ? "Searching…" : "Search now"}
            </button>
            <a className="button" href={`/movies/${id}/releases`} onClick={onLinkClick}>Find releases</a>
          </div>
        )}
        {file && !d && (
          <div className="button-row">
            {upgradable && (
              <button type="button" className="button primary" onClick={searchNow} disabled={busy === "search"}>
                {busy === "search" ? "Searching…" : "Look for Upgrade"}
              </button>
            )}
            <a className="small" href={`/movies/${id}/releases?replace=1`} onClick={onLinkClick}>Replace file…</a>
          </div>
        )}
      </section>

      {!file && (
        <section className="card section">
          <h2>Quality</h2>
          {qualityPicker}
        </section>
      )}
    </>
  );
}

function ago(iso: string) {
  const age = formatAge(iso);
  return age === "now" ? "Just now" : `${age} ago`;
}
