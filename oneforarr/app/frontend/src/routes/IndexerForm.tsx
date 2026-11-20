// Add or change one indexer. For a site, the form shows the settings its
// definition file lists; for a Torznab feed, an address and an API key.

import { useEffect, useState, type FormEvent } from "react";
import { Section, TestRow, TextField, errorText, type Status } from "../components/Form";
import {
  api, type DefinitionDetail, type DefinitionSetting, type Indexer, type IndexerInput, type Message,
} from "../lib/api";
import { navigate, onLinkClick } from "../lib/router";

// Props: either a new site (definitionId), a new feed (torznab), or an
// existing indexer (id).
type Props = { definitionId?: string; torznab?: boolean; id?: string };

const custom = "__custom__";

export function IndexerForm({ definitionId, torznab, id }: Props) {
  const [form, setForm] = useState<IndexerInput | null>(null);
  const [def, setDef] = useState<DefinitionDetail | null>(null);
  const [apiKeySet, setApiKeySet] = useState(false);
  const [customURL, setCustomURL] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState<Status>(null);
  const [testStatus, setTestStatus] = useState<Status>(null);
  const [busy, setBusy] = useState("");

  useEffect(() => {
    async function load() {
      try {
        let saved: Indexer | undefined;
        if (id) {
          const r = await api.get<{ indexers: Indexer[] }>("/api/indexers");
          saved = r.indexers.find((i) => i.id === id);
          if (!saved) throw new Error("That indexer doesn't exist (any more).");
          setApiKeySet(saved.apiKeySet);
        }
        const defId = saved?.definition ?? definitionId;
        if (defId && (saved?.kind ?? "definition") === "definition") {
          const d = await api.get<DefinitionDetail>(`/api/definitions/${defId}`);
          setDef(d);
          const settings: Record<string, string | boolean> = {};
          for (const s of d.settings) if (!s.type.startsWith("info")) settings[s.name] = saved?.settings[s.name] ?? s.default;
          const url = saved?.url ?? d.links[0] ?? "";
          setCustomURL(!d.links.includes(url));
          setForm({ kind: "definition", definition: d.id, name: saved?.name ?? d.name, enabled: saved?.enabled ?? true, url, apiKey: "", settings });
        } else {
          setForm({ kind: "torznab", definition: "", name: saved?.name ?? "", enabled: saved?.enabled ?? true, url: saved?.url ?? "", apiKey: "", settings: {} });
        }
      } catch (err) {
        setError(err instanceof Error && !("status" in err) ? err.message : errorText(err));
      }
    }
    load();
  }, [definitionId, torznab, id]);

  if (error && !form) return <p className="error">{error}</p>;
  if (!form) return <p className="muted">Loading…</p>;

  function set<K extends keyof IndexerInput>(key: K, value: IndexerInput[K]) {
    setForm((f) => (f ? { ...f, [key]: value } : f));
    setStatus(null);
  }
  function setSetting(name: string, value: string | boolean) {
    setForm((f) => (f ? { ...f, settings: { ...f.settings, [name]: value } } : f));
    setStatus(null);
  }

  async function test() {
    setBusy("test");
    setTestStatus(null);
    try {
      const r = await api.post<Message>("/api/indexers/test", { id: id ?? "", ...form });
      setTestStatus({ kind: "ok", text: r.message });
    } catch (err) {
      setTestStatus({ kind: "error", text: errorText(err) });
    } finally {
      setBusy("");
    }
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy("save");
    setStatus(null);
    try {
      if (id) await api.put(`/api/indexers/${id}`, form);
      else await api.post("/api/indexers", form);
      navigate("/indexers");
    } catch (err) {
      setStatus({ kind: "error", text: errorText(err) });
      setBusy("");
    }
  }

  async function remove() {
    if (!id || !window.confirm(`Remove ${form?.name}?`)) return;
    try {
      await api.del(`/api/indexers/${id}`);
      navigate("/indexers");
    } catch (err) {
      setStatus({ kind: "error", text: errorText(err) });
    }
  }

  const title = id ? form.name || "Indexer" : def ? `Add ${def.name}` : "Add Torznab feed";

  return (
    <form onSubmit={save}>
      <header className="page-head">
        <h1>{title}</h1>
        <a className="button" href="/indexers" onClick={onLinkClick}>Cancel</a>
      </header>

      <Section title={def ? "Site" : "Feed"} hint={def?.description}>
        <TextField label="Name" value={form.name} onChange={(v) => set("name", v)} />

        {def ? (
          <label className="field">
            <span>Site address</span>
            <select value={customURL ? custom : form.url}
              onChange={(e) => {
                if (e.target.value === custom) { setCustomURL(true); return; }
                setCustomURL(false);
                set("url", e.target.value);
              }}>
              {def.links.map((l) => <option key={l} value={l}>{l}</option>)}
              <option value={custom}>Another address…</option>
            </select>
            <small className="muted">If one address stops working, try the next. Sites move often.</small>
          </label>
        ) : null}
        {(!def || customURL) && (
          <TextField label={def ? "Other address" : "Feed address"} value={form.url} onChange={(v) => set("url", v)}
            placeholder={def ? "https://" : "http://host:9117/api/v2.0/indexers/x/results/torznab/"} inputMode="url" />
        )}
        {!def && (
          <TextField label="API key" type="password" value={form.apiKey} onChange={(v) => set("apiKey", v)}
            autoComplete="off" placeholder={apiKeySet ? "Saved — leave empty to keep" : ""} />
        )}

        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          Search this indexer
        </label>
      </Section>

      {def && def.settings.length > 0 && (
        <Section title="Options">
          {def.settings.map((s) => (
            <SettingField key={s.name} setting={s} value={form.settings[s.name]} onChange={(v) => setSetting(s.name, v)} />
          ))}
        </Section>
      )}

      <Section title="Check">
        <TestRow busy={busy === "test"} status={testStatus} onTest={test} />
      </Section>

      <div className="save-bar">
        {status && <span className="error" role="alert">{status.text}</span>}
        {id && <button type="button" className="button danger" onClick={remove}>Remove</button>}
        <button className="button primary" disabled={busy === "save"}>{busy === "save" ? "Saving…" : id ? "Save" : "Add"}</button>
      </div>
    </form>
  );
}

// SettingField draws one setting from a definition file.
function SettingField({ setting, value, onChange }: {
  setting: DefinitionSetting; value: string | boolean | undefined; onChange: (v: string | boolean) => void;
}) {
  const { type, label, options } = setting;
  if (type.startsWith("info")) {
    // Info text comes from the definition file and may hold simple HTML
    // like <b>; show it as plain text.
    return <p className="muted small">{stripTags(String(setting.default))}</p>;
  }
  if (type === "checkbox") {
    return (
      <label className="check">
        <input type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />
        {label}
      </label>
    );
  }
  if (type === "select" && options) {
    return (
      <label className="field">
        <span>{label}</span>
        <select value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}>
          {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      </label>
    );
  }
  return (
    <TextField label={label} value={String(value ?? "")} onChange={onChange}
      type={type === "password" ? "password" : "text"} autoComplete="off" />
  );
}

function stripTags(s: string) {
  return s.replace(/<br\s*\/?>/gi, " ").replace(/<[^>]*>/g, "");
}
