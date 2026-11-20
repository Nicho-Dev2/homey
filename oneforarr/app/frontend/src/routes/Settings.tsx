// Settings: qBittorrent, TMDB key, quality rules, automatic work, Jellyfin
// and folders. Passwords and keys are never
// shown; leave the box empty to keep the saved one.

import { useEffect, useState, type FormEvent } from "react";
import { Section, TestRow, TextField, errorText, type Status } from "../components/Form";
import { api, type Message, type Preset, type SettingsChange, type SettingsView } from "../lib/api";

function toChange(v: SettingsView): SettingsChange {
  return {
    qbittorrent: { url: v.qbittorrent.url, username: v.qbittorrent.username, password: "", category: v.qbittorrent.category,
      restartWhenStuck: v.qbittorrent.restartWhenStuck, giveUpHours: v.qbittorrent.giveUpHours },
    metadata: { tmdbKey: "", region: v.metadata.region },
    folders: { ...v.folders },
    movies: { ...v.movies },
    shows: { ...v.shows },
    schedule: { ...v.schedule },
    jellyfin: { url: v.jellyfin.url, apiKey: "" },
  };
}

export function Settings() {
  const [saved, setSaved] = useState<SettingsView | null>(null);
  const [form, setForm] = useState<SettingsChange | null>(null);
  const [loadError, setLoadError] = useState("");
  const [saveStatus, setSaveStatus] = useState<Status>(null);
  const [qbitStatus, setQbitStatus] = useState<Status>(null);
  const [tmdbStatus, setTmdbStatus] = useState<Status>(null);
  const [jellyfinStatus, setJellyfinStatus] = useState<Status>(null);
  const [restartStatus, setRestartStatus] = useState<Status>(null);
  const [busy, setBusy] = useState("");
  const [presets, setPresets] = useState<Preset[]>([]);

  useEffect(() => {
    api.get<Preset[]>("/api/movies/presets").then(setPresets).catch(() => {});
    api.get<SettingsView>("/api/settings")
      .then((v) => { setSaved(v); setForm(toChange(v)); })
      .catch((err) => setLoadError(errorText(err)));
  }, []);

  if (loadError) return <p className="error">{loadError}</p>;
  if (!form || !saved) return <p className="muted">Loading…</p>;

  // set changes one field, e.g. set("qbittorrent", "url", "http://…").
  function set<S extends keyof SettingsChange>(section: S, field: keyof SettingsChange[S], value: string | number | boolean) {
    setForm((f) => (f ? { ...f, [section]: { ...f[section], [field]: value } } : f));
    setSaveStatus(null);
  }

  async function run(name: string, path: string, show: (s: Status) => void) {
    setBusy(name);
    show(null);
    try {
      const res = await api.post<Message>(path, form);
      show({ kind: "ok", text: res.message });
    } catch (err) {
      show({ kind: "error", text: errorText(err) });
    } finally {
      setBusy("");
    }
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy("save");
    setSaveStatus(null);
    try {
      const v = await api.put<SettingsView>("/api/settings", form);
      setSaved(v);
      setForm(toChange(v));
      setSaveStatus({ kind: "ok", text: "Saved." });
    } catch (err) {
      setSaveStatus({ kind: "error", text: errorText(err) });
    } finally {
      setBusy("");
    }
  }

  return (
    <form onSubmit={save}>
      <header className="page-head">
        <h1>Settings</h1>
      </header>

      <Section title="qBittorrent" hint="qBittorrent runs inside the VPN container, so use gluetun's address.">
        <TextField label="Address" value={form.qbittorrent.url} onChange={(v) => set("qbittorrent", "url", v)}
          placeholder="http://gluetun:8080" inputMode="url" />
        <TextField label="Username" value={form.qbittorrent.username} onChange={(v) => set("qbittorrent", "username", v)}
          autoComplete="off" />
        <TextField label="Password" type="password" value={form.qbittorrent.password}
          onChange={(v) => set("qbittorrent", "password", v)} autoComplete="new-password"
          placeholder={saved.qbittorrent.passwordSet ? "Saved — leave empty to keep" : ""} />
        <TextField label="Category" value={form.qbittorrent.category} onChange={(v) => set("qbittorrent", "category", v)}
          help="Our downloads get this category, so the app only ever touches torrents it added." />
        <TestRow busy={busy === "qbit"} status={qbitStatus}
          onTest={() => run("qbit", "/api/settings/test/qbittorrent", setQbitStatus)} />
        <NumberField label="Give up on a download with no seeders after (hours)" value={form.qbittorrent.giveUpHours} step={1}
          onChange={(v) => set("qbittorrent", "giveUpHours", v)}
          help="No seeders and no progress for this long: it's removed from qBittorrent, that release is never picked again, and the app searches for another. Queued downloads don't count. 0 = never." />
        <Check label="Restart qBittorrent when it loses its connection" checked={form.qbittorrent.restartWhenStuck}
          onChange={(v) => set("qbittorrent", "restartWhenStuck", v)} />
        <p className="muted small">
          After a VPN reconnect qBittorrent can be left with 0 DHT nodes and stuck downloads. When that lasts 5 minutes,
          the app restarts it (at most every 15 minutes). The state shows on the Activity page.
        </p>
        <div className="button-row">
          <button type="button" className="button" disabled={busy === "restart"}
            onClick={() => run("restart", "/api/qbittorrent/restart", setRestartStatus)}>
            {busy === "restart" ? "Restarting…" : "Restart qBittorrent now"}
          </button>
          {restartStatus && <span className={restartStatus.kind === "ok" ? "ok small" : "error small"} role="status">{restartStatus.text}</span>}
        </div>
      </Section>

      <Section title="Movie and show details" hint="Titles, posters and descriptions come from TMDB. Episode lists come from TVmaze, which needs no key.">
        <TextField label="TMDB API key or read access token" type="password" value={form.metadata.tmdbKey}
          onChange={(v) => set("metadata", "tmdbKey", v)} autoComplete="off"
          placeholder={saved.metadata.tmdbKeySet ? "Saved — leave empty to keep" : ""}
          help={<>Free for personal use: create a TMDB account, then open <a href="https://www.themoviedb.org/settings/api" target="_blank" rel="noreferrer">Settings → API</a>.</>} />
        <TestRow busy={busy === "tmdb"} status={tmdbStatus}
          onTest={() => run("tmdb", "/api/settings/test/tmdb", setTmdbStatus)} />
        <TextField label="Country for Discover" value={form.metadata.region} onChange={(v) => set("metadata", "region", v)}
          autoComplete="off" help="Two letters, like US or GB: which streaming services the Discover page shows." />
      </Section>

      <Section title="Movies" hint="Which releases are good enough. Each movie can have its own quality too.">
        <label className="field">
          <span>Quality for new movies</span>
          <select value={form.movies.quality} onChange={(e) => set("movies", "quality", e.target.value)}>
            {presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </select>
        </label>
        <NumberField label="Largest download (GB)" value={form.movies.maxSizeGB} step={0.5}
          onChange={(v) => set("movies", "maxSizeGB", v)} help="0 means no limit. Remuxes are often 30–80 GB." />
        <NumberField label="Fewest seeders" value={form.movies.minSeeders} step={1}
          onChange={(v) => set("movies", "minSeeders", v)} />
        <Check label="Upgrade when a better release appears" checked={form.movies.upgrades}
          onChange={(v) => set("movies", "upgrades", v)} />
      </Section>

      <Section title="Shows" hint="The size limit is per episode; a season pack may be that times its number of episodes.">
        <label className="field">
          <span>Quality for new shows</span>
          <select value={form.shows.quality} onChange={(e) => set("shows", "quality", e.target.value)}>
            {presets.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </select>
        </label>
        <NumberField label="Largest download per episode (GB)" value={form.shows.maxEpisodeGB} step={0.5}
          onChange={(v) => set("shows", "maxEpisodeGB", v)} help="0 means no limit." />
        <NumberField label="Fewest seeders" value={form.shows.minSeeders} step={1}
          onChange={(v) => set("shows", "minSeeders", v)} />
        <Check label="Upgrade episodes when a better release appears" checked={form.shows.upgrades}
          onChange={(v) => set("shows", "upgrades", v)} />
      </Section>

      <Section title="Automatic" hint="What the app does on its own for wanted movies and episodes. 0 turns one off.">
        <NumberField label="Check the newest releases every (minutes)" value={form.schedule.rssMinutes} step={5}
          onChange={(v) => set("schedule", "rssMinutes", v)}
          help="One request per indexer. 5 minutes is the shortest allowed." />
        <NumberField label="Search for each wanted item every (hours)" value={form.schedule.searchHours} step={1}
          onChange={(v) => set("schedule", "searchHours", v)}
          help="One movie and one show at a time, oldest first. New episodes are searched soon after they air." />
      </Section>

      <Section title="Jellyfin" hint="Optional. After an import, Jellyfin is asked to scan, so new files show up right away.">
        <TextField label="Address" value={form.jellyfin.url} onChange={(v) => set("jellyfin", "url", v)}
          placeholder="http://localhost:8096" inputMode="url" help="Leave empty to turn this off." />
        <TextField label="API key" type="password" value={form.jellyfin.apiKey}
          onChange={(v) => set("jellyfin", "apiKey", v)} autoComplete="off"
          placeholder={saved.jellyfin.apiKeySet ? "Saved — leave empty to keep" : ""}
          help="In Jellyfin: Dashboard → API Keys → +." />
        <TestRow busy={busy === "jellyfin"} status={jellyfinStatus}
          onTest={() => run("jellyfin", "/api/settings/test/jellyfin", setJellyfinStatus)} />
      </Section>

      <Section title="Folders" hint="Paths as this app sees them. Keep all three on the same drive so finished downloads are linked instantly instead of copied.">
        <TextField label="Movies" value={form.folders.movies} onChange={(v) => set("folders", "movies", v)} />
        <TextField label="Shows" value={form.folders.shows} onChange={(v) => set("folders", "shows", v)} />
        <TextField label="Downloads" value={form.folders.downloads} onChange={(v) => set("folders", "downloads", v)} />
        <NumberField label="Keep this much free on the drive (GB)" value={form.folders.minFreeGB} step={10}
          onChange={(v) => set("folders", "minFreeGB", v)}
          help={`Nothing is sent to qBittorrent when it would leave less than this free. 0 turns the limit off.${saved.freeGB >= 0 ? ` Free now: ${saved.freeGB} GB.` : ""}`} />
      </Section>

      <Section title="Path mapping" hint="qBittorrent reports file paths as it sees them. If this app sees the same drive under another path, say so here. In Docker both are /data.">
        <TextField label="qBittorrent sees the drive as" value={form.folders.qbitRoot} onChange={(v) => set("folders", "qbitRoot", v)} />
        <TextField label="This app sees it as" value={form.folders.appRoot} onChange={(v) => set("folders", "appRoot", v)}
          help="Running on the Mac with make run: /Volumes/MyDrive/Media" />
      </Section>

      <div className="save-bar">
        {saveStatus && <span className={saveStatus.kind === "ok" ? "ok" : "error"} role="status">{saveStatus.text}</span>}
        <button className="button primary" disabled={busy === "save"}>{busy === "save" ? "Saving…" : "Save"}</button>
      </div>

      <Section title="About">
        <p className="muted small">
          Movie and show details and images come from <a href="https://www.themoviedb.org" target="_blank" rel="noreferrer">TMDB</a>.
          This product uses the TMDB API but is not endorsed or certified by TMDB.
        </p>
        <p className="muted small">
          Episode information comes from <a href="https://www.tvmaze.com" target="_blank" rel="noreferrer">TVmaze</a>, licensed CC BY-SA.
        </p>
      </Section>
    </form>
  );
}

function NumberField({ label, value, onChange, step, help }: {
  label: string; value: number; onChange: (v: number) => void; step: number; help?: string;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      <input type="number" inputMode="decimal" min={0} step={step} value={Number.isFinite(value) ? value : ""}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))} />
      {help && <small className="muted">{help}</small>}
    </label>
  );
}

function Check({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="check">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}
