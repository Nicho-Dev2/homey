// Indexers: the sites and feeds the app searches.

import { useEffect, useState } from "react";
import { errorText } from "../components/Form";
import { api, type Indexer, type IndexerInput } from "../lib/api";
import { navigate, onLinkClick } from "../lib/router";

export function Indexers() {
  const [list, setList] = useState<Indexer[] | null>(null);
  const [dir, setDir] = useState("");
  const [error, setError] = useState("");

  function load() {
    api.get<{ indexers: Indexer[]; definitionsDir: string }>("/api/indexers")
      .then((r) => { setList(r.indexers); setDir(r.definitionsDir); })
      .catch((err) => setError(errorText(err)));
  }
  useEffect(load, []);

  // Turning an indexer on or off saves at once.
  async function setEnabled(ix: Indexer, enabled: boolean) {
    setError("");
    try {
      const input: IndexerInput = {
        kind: ix.kind, definition: ix.definition ?? "", name: ix.name, enabled,
        url: ix.url, apiKey: "", settings: ix.settings,
      };
      await api.put(`/api/indexers/${ix.id}`, input);
      load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  return (
    <>
      <header className="page-head">
        <h1>Indexers</h1>
        <div className="head-actions">
          <a className="button" href="/indexers/search" onClick={onLinkClick}>Search</a>
          <a className="button primary" href="/indexers/add" onClick={onLinkClick}>Add</a>
        </div>
      </header>

      {error && <p className="error" role="alert">{error}</p>}
      {list === null && !error && <p className="muted">Loading…</p>}

      {list?.length === 0 && (
        <div className="card empty">
          <p>No indexers yet.</p>
          <p className="muted small">
            Put definition files (Prowlarr's <code>.yml</code> files) in <code>{dir}</code>, then add the sites you want.
          </p>
          <a className="button primary" href="/indexers/add" onClick={onLinkClick}>Add an indexer</a>
        </div>
      )}

      <ul className="list">
        {list?.map((ix) => (
          <li key={ix.id} className="card row-card">
            <button type="button" className="row-main" onClick={() => navigate(`/indexers/edit/${ix.id}`)}>
              <strong>{ix.name}</strong>
              <span className="muted small ellipsis">{ix.kind === "torznab" ? "Torznab feed · " : ""}{ix.url}</span>
            </button>
            <label className="switch" title={ix.enabled ? "On" : "Off"}>
              <input type="checkbox" checked={ix.enabled} onChange={(e) => setEnabled(ix, e.target.checked)}
                aria-label={`${ix.name} on`} />
              <span />
            </label>
          </li>
        ))}
      </ul>
    </>
  );
}
