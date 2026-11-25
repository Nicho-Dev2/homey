// One show: its seasons and episodes, what's followed and downloaded, and
// the buttons to search.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../components/Form";
import { CancelDownload, Poster, ProgressBar, StuckNote, formatETA } from "../components/MovieBits";
import { api, type Episode, type Message, type Preset, type Show, type ShowDownload } from "../lib/api";
import { formatAge, formatSize } from "../lib/format";
import { navigate, onLinkClick } from "../lib/router";

type Data = { show: Show; episodes: Episode[]; folder: string };
type Note = { ok: boolean; text: string };

export function ShowDetail({ id }: { id: string }) {
  const [data, setData] = useState<Data | null>(null);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [error, setError] = useState("");
  const [message, setMessage] = useState<Note | null>(null);
  // Searches run side by side: each button has its own busy state and note.
  const [running, setRunning] = useState<Set<string>>(new Set());
  const [notes, setNotes] = useState<Record<string, Note>>({});
  const [busy, setBusy] = useState("");
  const [open, setOpen] = useState<Set<number>>(new Set());
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [deleteFiles, setDeleteFiles] = useState(false);

  // Whether the newest season was opened yet: only on the first load, so a
  // season you close stays closed when the page refreshes every 5 seconds.
  const opened = useRef(false);
  const load = useCallback(() => {
    api.get<Data>(`/api/shows/${id}`)
      .then((d) => {
        setData(d);
        if (!opened.current && d.episodes.length) {
          opened.current = true;
          setOpen(new Set([Math.max(...d.episodes.map((e) => e.season))]));
        }
      })
      .catch((err) => setError(errorText(err)));
  }, [id]);

  useEffect(() => {
    load();
    api.get<Preset[]>("/api/movies/presets").then(setPresets).catch(() => {});
    const timer = window.setInterval(load, 5_000);
    return () => window.clearInterval(timer);
  }, [load]);

  const seasons = useMemo(() => {
    const by = new Map<number, Episode[]>();
    for (const e of data?.episodes ?? []) by.set(e.season, [...(by.get(e.season) ?? []), e]);
    return [...by.entries()].sort((a, b) => b[0] - a[0]); // newest first
  }, [data]);

  if (error && !data) return <p className="error">{error}</p>;
  if (!data) return <p className="muted">Loading…</p>;
  const { show, folder } = data;

  async function act(name: string, run: () => Promise<Message | unknown>) {
    setBusy(name);
    setMessage(null);
    try {
      const r = await run();
      if (r && typeof r === "object" && "message" in r) setMessage({ ok: true, text: String((r as Message).message) });
    } catch (err) {
      setMessage({ ok: false, text: errorText(err) });
    } finally {
      setBusy("");
      load();
    }
  }

  // search runs one search without blocking the others. Its result shows
  // next to its own button (an episode) or under Status (season, all).
  async function search(season = 0, episode = 0) {
    const key = `${season}-${episode}`;
    setRunning((r) => new Set(r).add(key));
    setNotes((n) => { const c = { ...n }; delete c[key]; return c; });
    let note: Note;
    try {
      const r = await api.post<Message>(`/api/shows/${id}/search`, { season, episode });
      note = { ok: true, text: r.message };
    } catch (err) {
      note = { ok: false, text: errorText(err) };
    }
    setRunning((r) => { const c = new Set(r); c.delete(key); return c; });
    setNotes((n) => ({ ...n, [key]: note }));
    load();
  }
  // downloadSeason follows every episode in the season first: asking for
  // the season means wanting all of it, and the episode-by-episode part of
  // the search only looks at followed episodes.
  async function downloadSeason(season: number) {
    try {
      await api.post(`/api/shows/${id}/monitor`, { season, episode: 0, monitored: true });
    } catch (err) {
      setNotes((n) => ({ ...n, [`${season}-0`]: { ok: false, text: errorText(err) } }));
      return;
    }
    await search(season, 0);
  }
  const monitor = (season: number, episode: number, monitored: boolean) =>
    act("monitor", () => api.post(`/api/shows/${id}/monitor`, { season, episode, monitored }));
  const toggle = (season: number) =>
    setOpen((o) => { const n = new Set(o); if (n.has(season)) n.delete(season); else n.add(season); return n; });

  async function remove() {
    setBusy("remove");
    try {
      await api.del(`/api/shows/${id}`, { deleteFiles });
      navigate("/shows");
    } catch (err) {
      setMessage({ ok: false, text: errorText(err) });
      setBusy("");
    }
  }

  return (
    <>
      <header className="page-head">
        <a className="button" href="/shows" onClick={onLinkClick}>← Shows</a>
      </header>

      <section className="movie-head">
        <div className="movie-poster"><Poster path={show.posterPath} title={show.title} width={342} /></div>
        <div className="movie-info">
          <h1>{show.title} <span className="muted">{show.year || ""}</span></h1>
          <p className="muted small">
            {show.status ? `${show.status} · ` : ""}
            <a href={`https://www.themoviedb.org/tv/${show.tmdbId}`} target="_blank" rel="noreferrer">TMDB</a>
            {show.tvmazeId ? <> · <a href={`https://www.tvmaze.com/shows/${show.tvmazeId}`} target="_blank" rel="noreferrer">TVmaze</a></> : null}
          </p>
          {show.overview && <p className="overview">{show.overview}</p>}
          <div className="button-row">
            <button type="button" className="button primary" onClick={() => search()} disabled={running.has("0-0")}>
              {running.has("0-0") ? "Searching…" : "Search missing"}
            </button>
            <button type="button" className="button" disabled={busy === "refresh"}
              onClick={() => act("refresh", () => api.post<Message>(`/api/shows/${id}/refresh`))}>
              {busy === "refresh" ? "Updating…" : "Update episode list"}
            </button>
          </div>
          {message && <p className={message.ok ? "ok small" : "error small"} role="status">{message.text}</p>}
          {notes["0-0"] && <p className={notes["0-0"].ok ? "ok small" : "error small"} role="status">{notes["0-0"].text}</p>}
          {show.lastSearch && <p className="muted small">Last search {ago(show.lastSearch.at).toLowerCase()}: {show.lastSearch.message}</p>}
          {show.refreshError && <p className="error small">Episode list: {show.refreshError}</p>}
          <p className="muted small">Files go in <code>{folder}</code>. Episode list updated {ago(show.refreshedAt).toLowerCase()}.</p>
        </div>
      </section>


      {seasons.map(([season, eps]) => {
        const have = eps.filter((e) => e.file).length;
        const seasonDownloads = show.downloads.filter((d) => d.season === season);
        const packs = seasonDownloads.filter((d) => !d.episode);
        const cancelled = (text: string, ok: boolean) => {
          setMessage({ text: ok ? `${text} Those episodes are no longer followed.` : text, ok });
          load();
        };
        return (
          <section key={season} className="card season">
            <div className="season-head">
              <button type="button" className="season-title" onClick={() => toggle(season)} aria-expanded={open.has(season)}>
                <strong>Season {season}</strong>
                <span className="muted small">
                  {have}/{eps.length}{seasonDownloads.length ? ` · ${seasonDownloads.length} downloading` : ""}
                </span>
              </button>
              <div className="season-buttons">
                <button type="button" className="button small-button" onClick={() => downloadSeason(season)} disabled={running.has(`${season}-0`)}
                  title="Follows every episode, then sends the best season pack, or else each episode you don't have yet">
                  {running.has(`${season}-0`) ? "Searching…" : "Download Season"}
                </button>
              </div>
              <button type="button" className="season-caret" onClick={() => toggle(season)} aria-expanded={open.has(season)}
                aria-label={`${open.has(season) ? "Hide" : "Show"} season ${season} episodes`}>
                {open.has(season) ? "▾" : "▸"}
              </button>
            </div>
            {notes[`${season}-0`] && (
              <p className={notes[`${season}-0`].ok ? "ok small" : "error small"} role="status">{notes[`${season}-0`].text}</p>
            )}
            {open.has(season) && (
              <>
                {packs.map((d) => (
                  <div key={d.tag} className="episode pack-row">
                    <div className="episode-main">
                      <span className="episode-title">Season pack</span>
                      <span className="muted small ellipsis">{d.releaseTitle}</span>
                      <DownloadLine show={show.tmdbId} d={d} onCancelled={cancelled} />
                    </div>
                  </div>
                ))}
                <label className="episode follow-all">
                  <FollowAll eps={eps} onChange={(on) => monitor(season, 0, on)} />
                  <span>Follow All Episodes</span>
                </label>
                <ul className="episodes">
                  {eps.map((e) => (
                    <EpisodeRow key={e.number} e={e} busy={running.has(`${e.season}-${e.number}`)}
                      show={show.tmdbId} onCancelled={cancelled}
                      download={seasonDownloads.find((d) => d.episode === e.number)} inPack={packs.length > 0}
                      note={notes[`${e.season}-${e.number}`]}
                      onMonitor={(on) => monitor(e.season, e.number, on)} onSearch={() => search(e.season, e.number)}
                      releasesHref={`/shows/${id}/releases?season=${e.season}&episode=${e.number}`} />
                  ))}
                </ul>
              </>
            )}
          </section>
        );
      })}

      <section className="card section">
        <h2>Quality</h2>
        <select value={show.quality} aria-label="Quality"
          onChange={(e) => act("quality", () => api.put(`/api/shows/${id}`, { quality: e.target.value }))}>
          {presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
        </select>
      </section>

      <section className="card section">
        <h2>Remove</h2>
        {!confirmRemove ? (
          <div><button type="button" className="button danger" onClick={() => setConfirmRemove(true)}>Remove show…</button></div>
        ) : (
          <>
            <label className="check">
              <input type="checkbox" checked={deleteFiles} onChange={(e) => setDeleteFiles(e.target.checked)} />
              Also delete <code>{folder}</code> from the TV folder
            </label>
            <p className="muted small">Its downloads still in progress are cancelled and deleted from qBittorrent. Finished ones are left there, so they can keep seeding.</p>
            <div className="button-row">
              <button type="button" className="button" onClick={() => setConfirmRemove(false)}>Keep it</button>
              <button type="button" className="button danger" onClick={remove} disabled={busy === "remove"}>Remove</button>
            </div>
          </>
        )}
      </section>
    </>
  );
}

// FollowAll ticks or clears every episode's checkbox in a season. It shows
// a dash when only some are followed.
function FollowAll({ eps, onChange }: { eps: Episode[]; onChange: (on: boolean) => void }) {
  const box = useRef<HTMLInputElement>(null);
  const on = eps.filter((e) => e.monitored).length;
  useEffect(() => {
    if (box.current) box.current.indeterminate = on > 0 && on < eps.length;
  }, [on, eps.length]);
  return <input ref={box} type="checkbox" checked={on === eps.length} onChange={(e) => onChange(e.target.checked)} />;
}

// DownloadLine is a download's progress bar, numbers and Cancel button on
// one line, with any problem below.
function DownloadLine({ show, d, onCancelled }: { show: number; d: ShowDownload; onCancelled: (text: string, ok: boolean) => void }) {
  return (
    <>
      <div className="dl-line">
        <ProgressBar value={d.progress} />
        <span className="muted small" title={d.size ? `${formatSize(d.size)} · ${d.releaseTitle}` : d.releaseTitle}>
          {Math.floor(d.progress * 100)}% {formatETA(d.eta)}
        </span>
        <CancelDownload kind="show" id={show} tag={d.tag} onDone={onCancelled} short />
      </div>
      {d.error && <span className="error small">{d.error}</span>}
      <StuckNote d={d} />
    </>
  );
}

function EpisodeRow({ e, busy, note, onMonitor, onSearch, releasesHref, show, download, inPack, onCancelled }: {
  e: Episode; busy: boolean; note?: Note; onMonitor: (on: boolean) => void; onSearch: () => void; releasesHref: string;
  show: number; download?: ShowDownload; inPack: boolean; onCancelled: (text: string, ok: boolean) => void;
}) {
  let status: { text: string; cls: string };
  if (e.file) status = { text: e.file.quality === "Unknown" ? "Downloaded" : e.file.quality, cls: "ok" };
  else if (e.downloading) status = { text: download || !inPack ? "Downloading" : "Downloading in the season pack", cls: "busy-text" };
  else if (!e.aired) status = { text: e.airDate ? `Airs ${e.airDate}` : "Not announced", cls: "muted" };
  else if (e.monitored) status = { text: "Missing", cls: "error" };
  else status = { text: "Not followed", cls: "muted" };

  return (
    <li className="episode">
      <input type="checkbox" checked={e.monitored} onChange={(ev) => onMonitor(ev.target.checked)}
        aria-label={`Follow episode ${e.number}`} />
      <div className="episode-main">
        <span className="episode-title"><span className="muted">{e.number}.</span> {e.title || "TBA"}</span>
        <span className="small"><span className={status.cls}>{busy ? "Searching…" : status.text}</span>
          {e.aired && e.airDate ? <span className="muted"> · {e.airDate}</span> : null}</span>
        {download && <DownloadLine show={show} d={download} onCancelled={onCancelled} />}
        {note && !e.downloading && !e.file && <span className={note.ok ? "ok small" : "error small"}>{note.text}</span>}
      </div>
      {e.aired && !e.file && !e.downloading && (
        <div className="episode-actions">
          <button type="button" className="link-button small" onClick={onSearch} disabled={busy}>{busy ? "…" : "Search"}</button>
          <a className="small" href={releasesHref} onClick={onLinkClick}>Releases</a>
        </div>
      )}
    </li>
  );
}

function ago(iso: string) {
  const age = formatAge(iso);
  return age === "now" ? "Just now" : `${age} ago`;
}
