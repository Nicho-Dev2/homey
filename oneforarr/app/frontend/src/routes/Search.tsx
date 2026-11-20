// Search every indexer at once and send a result to qBittorrent: the same
// as Prowlarr's search page. Movies and Shows will search automatically.

import { useMemo, useState, type FormEvent } from "react";
import { errorText } from "../components/Form";
import { api, type Message, type Release, type SearchKind, type SearchResults } from "../lib/api";
import { categoryLabel, formatAge, formatSize } from "../lib/format";
import { onLinkClick } from "../lib/router";

type Sort = "seeders" | "size" | "age";

const kindCategories: Record<SearchKind, number[]> = { search: [], movie: [2000], tv: [5000] };

export function Search() {
  const [text, setText] = useState("");
  const [kind, setKind] = useState<SearchKind>("search");
  const [results, setResults] = useState<SearchResults | null>(null);
  const [sort, setSort] = useState<Sort>("seeders");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function search(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setResults(null);
    try {
      setResults(await api.post<SearchResults>("/api/search", { kind, text, categories: kindCategories[kind] }));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  const sorted = useMemo(() => {
    const list = [...(results?.releases ?? [])];
    if (sort === "size") list.sort((a, b) => b.size - a.size);
    if (sort === "age") list.sort((a, b) => (b.publishedAt ?? "").localeCompare(a.publishedAt ?? ""));
    return list;
  }, [results, sort]);

  return (
    <>
      <header className="page-head">
        <h1>Search</h1>
        <a className="button" href="/indexers" onClick={onLinkClick}>Indexers</a>
      </header>

      <form className="card search-form" onSubmit={search}>
        <input type="search" placeholder="Title, e.g. Night of the Living Dead 1968" value={text}
          onChange={(e) => setText(e.target.value)} aria-label="Search for" enterKeyHint="search" />
        <div className="search-controls">
          <select value={kind} onChange={(e) => setKind(e.target.value as SearchKind)} aria-label="Kind">
            <option value="search">Everything</option>
            <option value="movie">Movies</option>
            <option value="tv">TV</option>
          </select>
          <button className="button primary" disabled={busy}>{busy ? "Searching…" : "Search"}</button>
        </div>
      </form>

      {error && <p className="error" role="alert">{error}</p>}

      {results && (
        <>
          {results.failures.map((f) => (
            <p key={f.indexerId} className="error small">{f.indexerName}: {f.error}</p>
          ))}
          <div className="results-head">
            <span className="muted">{results.releases.length} results</span>
            <select value={sort} onChange={(e) => setSort(e.target.value as Sort)} aria-label="Sort by">
              <option value="seeders">Most seeders</option>
              <option value="size">Largest</option>
              <option value="age">Newest</option>
            </select>
          </div>
          <ul className="list">
            {sorted.map((r, i) => <ResultCard key={`${r.indexerId}-${i}-${r.title}`} r={r} />)}
          </ul>
        </>
      )}
    </>
  );
}

function ResultCard({ r }: { r: Release }) {
  const [state, setState] = useState<"idle" | "busy" | "sent" | "error">("idle");
  const [message, setMessage] = useState("");

  async function send() {
    setState("busy");
    try {
      const res = await api.post<Message>("/api/search/send", {
        indexerId: r.indexerId, downloadUrl: r.downloadUrl ?? "", magnetUrl: r.magnetUrl ?? "", size: r.size ?? 0,
      });
      setState("sent");
      setMessage(res.message);
    } catch (err) {
      setState("error");
      setMessage(errorText(err));
    }
  }

  const cat = categoryLabel(r.categories);
  return (
    <li className="card result">
      <div className="result-title">{r.title}</div>
      <div className="result-meta muted small">
        <span>{r.indexerName}</span>
        {cat && <span>{cat}</span>}
        <span>{formatSize(r.size)}</span>
        <span className={r.seeders > 0 ? "ok" : ""}>▲ {r.seeders}</span>
        <span>▼ {r.leechers}</span>
        <span>{formatAge(r.publishedAt)}</span>
      </div>
      <div className="result-actions">
        {r.detailsUrl && <a className="button" href={r.detailsUrl} target="_blank" rel="noreferrer noopener">Details</a>}
        <button type="button" className="button primary" onClick={send} disabled={state === "busy" || state === "sent"}>
          {state === "busy" ? "Sending…" : state === "sent" ? "Sent" : "Send to qBittorrent"}
        </button>
        {message && <span className={state === "error" ? "error small" : "ok small"} role="status">{message}</span>}
      </div>
    </li>
  );
}
