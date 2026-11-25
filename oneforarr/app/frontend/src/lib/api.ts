// Every call to the Go backend goes through here.

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

// request sends JSON and returns the JSON answer. An error answer throws
// ApiError with the backend's message, which is written to be shown as is.
async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const message = typeof data.error === "string" ? data.error : `Something went wrong (${res.status}).`;
    throw new ApiError(message, res.status);
  }
  return data as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body: unknown = {}) => request<T>("POST", path, body),
  put: <T>(path: string, body: unknown) => request<T>("PUT", path, body),
  del: <T>(path: string, body: unknown = {}) => request<T>("DELETE", path, body),
};

// ---- Types shared with the backend ----

export type Me = {
  needsSetup: boolean;
  signedIn: boolean;
  username: string;
};

export type Folders = {
  movies: string;
  shows: string;
  downloads: string;
  qbitRoot: string; // the drive as qBittorrent sees it
  appRoot: string; // the same drive as this app sees it
  minFreeGB: number; // nothing is sent when it would leave less free; 0 = off
};

export type MovieRules = {
  quality: string;
  maxSizeGB: number;
  minSeeders: number;
  upgrades: boolean; // replace a file below the wanted quality
};

export type ShowRules = {
  quality: string;
  maxEpisodeGB: number;
  minSeeders: number;
  upgrades: boolean;
};

// Schedule: 0 turns either one off.
export type Schedule = { rssMinutes: number; searchHours: number };

// SettingsView is what GET /api/settings returns (no secrets).
export type SettingsView = {
  qbittorrent: { url: string; username: string; passwordSet: boolean; category: string; restartWhenStuck: boolean; giveUpHours: number };
  metadata: { tmdbKeySet: boolean; region: string };
  folders: Folders;
  movies: MovieRules;
  shows: ShowRules;
  schedule: Schedule;
  jellyfin: { url: string; apiKeySet: boolean };
  freeGB: number; // free space on the downloads drive now; -1 unknown
};

// SettingsChange is what PUT /api/settings takes. An empty password,
// tmdbKey or Jellyfin apiKey keeps the saved one.
export type SettingsChange = {
  qbittorrent: { url: string; username: string; password: string; category: string; restartWhenStuck: boolean; giveUpHours: number };
  metadata: { tmdbKey: string; region: string };
  folders: Folders;
  movies: MovieRules;
  shows: ShowRules;
  schedule: Schedule;
  jellyfin: { url: string; apiKey: string };
};

export type Message = { message: string };

// ---- Indexers ----

export type IndexerKind = "definition" | "torznab";

export type Indexer = {
  id: string;
  kind: IndexerKind;
  definition?: string;
  name: string;
  enabled: boolean;
  url: string;
  apiKeySet: boolean;
  settings: Record<string, string | boolean>;
};

// IndexerInput is what the Add/Edit form sends. Empty apiKey keeps the saved one.
export type IndexerInput = {
  kind: IndexerKind;
  definition: string;
  name: string;
  enabled: boolean;
  url: string;
  apiKey: string;
  settings: Record<string, string | boolean>;
};

export type DefinitionSummary = {
  id: string;
  name: string;
  description: string;
  language: string;
  type: string;
  supported: boolean;
  reason?: string;
};

export type DefinitionSetting = {
  name: string;
  type: string; // text, password, checkbox, select, info…
  label: string;
  default: string | boolean;
  options?: { value: string; label: string }[];
};

export type DefinitionDetail = DefinitionSummary & {
  links: string[];
  settings: DefinitionSetting[];
};

// ---- Search ----

export type SearchKind = "search" | "movie" | "tv";

export type Query = {
  kind: SearchKind;
  text: string;
  imdbId?: string;
  season?: number;
  episode?: number;
  categories?: number[];
};

export type Release = {
  indexerId: string;
  indexerName: string;
  title: string;
  size: number;
  seeders: number;
  leechers: number;
  grabs?: number;
  publishedAt?: string;
  categories: number[];
  imdbId?: string;
  detailsUrl?: string;
  downloadUrl?: string;
  magnetUrl?: string;
  infoHash?: string;
};

export type SearchResults = {
  releases: Release[];
  failures: { indexerId: string; indexerName: string; error: string }[];
};

// ---- Movies ----

export type Download = {
  tag: string;
  releaseTitle: string;
  indexerName: string;
  link?: string; // "torrent file", "magnet" or "magnet without trackers"
  stuckSince?: string; // no seeders and no progress since then
  replace?: boolean; // picked by hand to replace the file in the library
  sentAt: string;
  hash?: string;
  size?: number;
  progress: number; // 0..1
  state?: string;
  eta?: number; // seconds
  error?: string;
};

export type Movie = {
  tmdbId: number;
  imdbId?: string;
  title: string;
  originalTitle?: string;
  year: number;
  overview?: string;
  posterPath?: string;
  backdropPath?: string;
  runtime?: number;
  releaseDate?: string; // "2026-10-01"
  quality: string;
  addedAt: string;
  file?: { path: string; size: number; quality: string; importedAt: string };
  download?: Download;
  lastSearch?: { at: string; message: string };
};

export type MovieLookup = {
  tmdbId: number;
  title: string;
  year: number;
  overview: string;
  posterPath: string;
  inLibrary: boolean;
};

export type Preset = { id: string; label: string };

export type Decision = {
  release: Release;
  parsed: { title: string; year?: number; resolution?: number; source: string };
  quality: string;
  accepted: boolean;
  reasons: string[];
};

export type ActivityItem = {
  kind: "movie" | "show";
  id: number;
  title: string;
  year?: number;
  label?: string; // "S01E02", "Season 1"
  posterPath?: string;
  download: Download;
};

// ---- Discover ----

export type DiscoverItem = {
  kind: "movie" | "show";
  tmdbId: number;
  title: string;
  year: number;
  overview: string;
  posterPath: string;
  backdropPath: string;
  rating: number; // 0–10
  votes: number;
  releaseDate?: string; // "2026-10-01"; a show's first air date
  inApp: boolean; // added already, not downloaded yet
};

export type DiscoverRow = { id: string; title: string; logoPath?: string; items: DiscoverItem[] };

export type DiscoverPage = { region: string; updatedAt: string; rows: DiscoverRow[]; problems?: string[] };

// ---- qBittorrent watchdog ----

export type QBitHealth = {
  checkedAt?: string;
  reachable: boolean;
  error?: string;
  dhtNodes: number; // -1 when DHT is off
  status?: string; // connected, firewalled, disconnected
  problem?: string;
  badSince?: string;
  restarted?: string;
  reason?: string;
};

// ---- Library (folder scan) ----

export type Unmatched = {
  kind: "movie" | "show";
  path: string;
  name: string; // folder or file name
  title: string;
  year?: number;
  note: string; // why it wasn't added
};

export type LibraryView = {
  lastScan?: string;
  report?: string;
  unmatched: Unmatched[];
  skipped: string[];
  scanning: boolean;
};

// ---- Shows ----

export type MediaFile = { path: string; size: number; quality: string; importedAt: string };

export type ShowSummary = {
  tmdbId: number;
  title: string;
  year: number;
  posterPath?: string;
  status?: string;
  episodes: number; // followed episodes that have aired
  have: number;
  downloading: number;
};

export type Episode = {
  season: number;
  number: number;
  title: string;
  airDate?: string;
  airStamp?: string;
  runtime?: number;
  overview?: string;
  monitored: boolean;
  file?: MediaFile;
  aired: boolean;
  downloading: boolean;
};

export type ShowDownload = Download & { season: number; episode?: number };

export type Show = {
  tmdbId: number;
  tvdbId?: number;
  tvmazeId?: number;
  imdbId?: string;
  title: string;
  year: number;
  overview?: string;
  posterPath?: string;
  status?: string;
  quality: string;
  monitor: string;
  refreshedAt: string;
  refreshError?: string;
  downloads: ShowDownload[];
  lastSearch?: { at: string; message: string };
};

// A search target: one episode, or a whole season (episode 0).
export type Target = { season: number; episode: number };

// posterURL is a TMDB poster image address, or "" when there's none.
export function posterURL(path?: string, width: 185 | 342 | 500 = 342) {
  return path ? `https://image.tmdb.org/t/p/w${width}${path}` : "";
}
