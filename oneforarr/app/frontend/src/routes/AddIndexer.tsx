// Add an indexer: pick a site from the definition files, or a Torznab feed.

import { useEffect, useMemo, useState } from "react";
import { errorText } from "../components/Form";
import { api, type DefinitionSummary } from "../lib/api";
import { navigate, onLinkClick } from "../lib/router";

export function AddIndexer() {
  const [defs, setDefs] = useState<DefinitionSummary[] | null>(null);
  const [dir, setDir] = useState("");
  const [filter, setFilter] = useState("");
  const [showAll, setShowAll] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    api.get<{ definitions: DefinitionSummary[]; dir: string }>("/api/definitions")
      .then((r) => { setDefs(r.definitions); setDir(r.dir); })
      .catch((err) => setError(errorText(err)));
  }, []);

  const shown = useMemo(() => {
    const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
    return (defs ?? []).filter((d) =>
      (showAll || d.supported) &&
      words.every((w) => `${d.name} ${d.description} ${d.id}`.toLowerCase().includes(w)));
  }, [defs, filter, showAll]);

  const supportedCount = defs?.filter((d) => d.supported).length ?? 0;

  return (
    <>
      <header className="page-head">
        <h1>Add indexer</h1>
        <a className="button" href="/indexers" onClick={onLinkClick}>Cancel</a>
      </header>

      <button type="button" className="card row-card wide-button" onClick={() => navigate("/indexers/add/torznab")}>
        <span className="row-main">
          <strong>Torznab or Newznab feed</strong>
          <span className="muted small">Any feed address with an API key, e.g. from Jackett.</span>
        </span>
      </button>

      <h2 className="list-title">Sites from definition files</h2>
      {error && <p className="error" role="alert">{error}</p>}
      {defs === null && !error && <p className="muted">Loading…</p>}
      {defs?.length === 0 && (
        <p className="muted small">No definition files found. Copy Prowlarr's <code>.yml</code> files into <code>{dir}</code>.</p>
      )}

      {defs && defs.length > 0 && (
        <div className="filter-bar">
          <input type="search" placeholder="Filter sites" value={filter} onChange={(e) => setFilter(e.target.value)}
            aria-label="Filter sites" />
          <label className="check">
            <input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} />
            Show sites that can't be used yet ({defs.length - supportedCount})
          </label>
        </div>
      )}

      <ul className="list">
        {shown.map((d) => (
          <li key={d.id}>
            <button type="button" className="card row-card wide-button" disabled={!d.supported}
              onClick={() => navigate(`/indexers/add/${d.id}`)}>
              <span className="row-main">
                <strong>{d.name}</strong>
                <span className="muted small">{d.supported ? d.description : d.reason}</span>
              </span>
              <span className="muted small">{d.language}</span>
            </button>
          </li>
        ))}
      </ul>
    </>
  );
}
