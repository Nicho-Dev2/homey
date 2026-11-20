// Activity: what's downloading now. Refreshes on its own.

import { useEffect, useState } from "react";
import { errorText } from "../components/Form";
import { CancelDownload, Poster, ProgressBar, StuckNote, formatETA } from "../components/MovieBits";
import { api, type ActivityItem, type QBitHealth } from "../lib/api";
import { formatAge, formatSize } from "../lib/format";
import { onLinkClick } from "../lib/router";

export function Activity() {
  const [items, setItems] = useState<ActivityItem[] | null>(null);
  const [error, setError] = useState("");
  const [health, setHealth] = useState<QBitHealth | null>(null);
  const [notice, setNotice] = useState<{ text: string; ok: boolean } | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const load = () => {
      api.get<ActivityItem[]>("/api/activity")
        .then((r) => { setItems(r); setError(""); })
        .catch((err) => setError(errorText(err)));
      api.get<QBitHealth>("/api/qbittorrent/health").then(setHealth).catch(() => {});
    };
    load();
    const timer = window.setInterval(load, 5_000);
    return () => window.clearInterval(timer);
  }, [reload]);

  return (
    <>
      <header className="page-head">
        <h1>Activity</h1>
      </header>
      {health && <HealthNote h={health} />}
      {error && <p className="error" role="alert">{error}</p>}
      {notice && <p className={notice.ok ? "ok" : "error"} role="status">{notice.text}</p>}
      {items === null && !error && <p className="muted">Loading…</p>}
      {items?.length === 0 && (
        <div className="card empty">
          <p>Nothing downloading.</p>
          <p className="muted small">Progress is checked with qBittorrent every 30 seconds.</p>
        </div>
      )}
      <ul className="list">
        {items?.map((it) => (
          <li key={it.download.tag} className="activity-item">
            <a className="card activity" href={`/${it.kind === "show" ? "shows" : "movies"}/${it.id}`} onClick={onLinkClick}>
              <div className="activity-poster"><Poster path={it.posterPath} title={it.title} width={185} /></div>
              <div className="activity-body">
                <strong>{it.title} <span className="muted">{it.label ?? (it.year || "")}</span></strong>
                <span className="muted small ellipsis">{it.download.releaseTitle}</span>
                <span className="muted small">
                  {it.download.indexerName}{it.download.link ? ` · ${it.download.link}` : ""}
                </span>
                {it.download.link === "magnet without trackers" && it.download.progress === 0 && (
                  <span className="muted small">Without trackers it finds peers through DHT only, so it can take a while to start.</span>
                )}
                <ProgressBar value={it.download.progress} />
                <span className="muted small">
                  {Math.floor(it.download.progress * 100)}%
                  {it.download.size ? ` of ${formatSize(it.download.size)}` : ""} {formatETA(it.download.eta)}
                </span>
                {it.download.error && <span className="error small">{it.download.error}</span>}
                <StuckNote d={it.download} />
              </div>
            </a>
            <div className="activity-actions">
              <CancelDownload kind={it.kind === "show" ? "show" : "movie"} id={it.id} tag={it.download.tag}
                onDone={(text, ok) => { setNotice({ text: ok ? `${it.title}: ${text}` : text, ok }); setReload((n) => n + 1); }} />
            </div>
          </li>
        ))}
      </ul>
    </>
  );
}

// HealthNote is one line about qBittorrent's connection, from the watchdog.
function HealthNote({ h }: { h: QBitHealth }) {
  if (!h.checkedAt) return null;
  if (!h.reachable) {
    return (
      <p className="card error small" role="alert">
        Can't reach qBittorrent: {h.error} If the VPN container restarted, run{" "}
        <code>docker compose restart qbittorrent</code> in the arr-stack folder.
      </p>
    );
  }
  if (h.problem) {
    return (
      <p className="card error small" role="alert">
        {h.problem} (for {since(h.badSince)}). It's restarted after 5 minutes when automatic restarts are on in Settings.
      </p>
    );
  }
  const recent = h.restarted && Date.now() - new Date(h.restarted).getTime() < 6 * 3600_000;
  return (
    <p className="muted small">
      qBittorrent: {h.status}{h.dhtNodes >= 0 ? ` · ${h.dhtNodes} DHT nodes` : ""}
      {recent ? ` · restarted ${since(h.restarted)} ago (${h.reason})` : ""}
    </p>
  );
}

function since(iso?: string): string {
  const age = formatAge(iso);
  return age === "now" ? "under a minute" : age;
}
