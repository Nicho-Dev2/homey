// Small pieces the movie and show pages share: poster, status badge,
// progress bar, the Cancel button and the "no seeders" note.

import { useEffect, useState } from "react";
import { api, posterURL, type Download, type Movie } from "../lib/api";
import { formatAge } from "../lib/format";

export function Poster({ path, title, width = 342 }: { path?: string; title: string; width?: 185 | 342 | 500 }) {
  const src = posterURL(path, width);
  if (!src) return <div className="poster poster-empty" aria-label={title}><span>{title}</span></div>;
  return <img className="poster" src={src} alt={title} loading="lazy" />;
}

export type Status = { label: string; kind: "done" | "busy" | "wanted" | "problem" };

export function movieStatus(m: Movie): Status {
  if (m.download?.error) return { label: "Problem", kind: "problem" };
  if (m.download) return { label: `${Math.floor(m.download.progress * 100)}%`, kind: "busy" };
  if (m.file) return { label: m.file.quality === "Unknown" ? "Downloaded" : m.file.quality, kind: "done" };
  return { label: "Wanted", kind: "wanted" };
}

export function Badge({ status }: { status: Status }) {
  return <span className={`badge badge-${status.kind}`}>{status.label}</span>;
}

export function ProgressBar({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(100, value * 100));
  return (
    <div className="progress" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
      <div style={{ width: `${pct}%` }} />
    </div>
  );
}

// formatETA turns seconds into "2 h 5 min"; qBittorrent's 8640000 means unknown.
export function formatETA(s?: number) {
  if (!s || s >= 8640000) return "";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h} h ${m} min left` : `${Math.max(1, m)} min left`;
}

// CancelDownload stops a download: the torrent and its partial files are
// removed from qBittorrent. It asks for a second tap instead of a pop-up.
// For a show, the episodes it was for stop being followed.
export function CancelDownload({ kind, id, tag, onDone, short }: {
  kind: "movie" | "show"; id: number; tag: string; onDone: (message: string, ok: boolean) => void; short?: boolean;
}) {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!armed) return;
    const timer = window.setTimeout(() => setArmed(false), 4000);
    return () => window.clearTimeout(timer);
  }, [armed]);

  async function click() {
    if (!armed) {
      setArmed(true);
      return;
    }
    setBusy(true);
    try {
      const path = kind === "movie" ? `/api/movies/${id}/cancel` : `/api/shows/${id}/cancel`;
      const res = await api.post<{ message: string }>(path, kind === "movie" ? {} : { tag });
      onDone(res.message, true);
    } catch (err) {
      onDone(err instanceof Error ? err.message : String(err), false);
    } finally {
      setBusy(false);
      setArmed(false);
    }
  }

  return (
    <button type="button" className={armed ? "button danger" : "button"} onClick={click} disabled={busy}>
      {busy ? "Cancelling…" : armed ? (short ? "Sure?" : "Tap again to cancel") : (short ? "Cancel" : "Cancel Download")}
    </button>
  );
}

// StuckNote says how long a download has gone without seeders or progress.
export function StuckNote({ d }: { d: Download }) {
  // Shown after half an hour: a torrent often takes a few minutes to find peers.
  if (!d.stuckSince || Date.now() - new Date(d.stuckSince).getTime() < 30 * 60_000) return null;
  return (
    <span className="error small">
      No seeders and no progress for {formatAge(d.stuckSince)}. The app gives up on it after the hours set in Settings and searches for another release.
    </span>
  );
}
