# OneForArr

One app for **Movies**, **Shows** and **Indexers**: it does the work of Radarr, Sonarr and Prowlarr together, and sends downloads to qBittorrent.

- **Backend:** Go. One program; the web interface is built into it. Outside packages: a YAML reader, Go's HTML parser and a CSS-selector library (for indexer definition files).
- **Front end:** React + TypeScript, built for phones and desktops alike.
- **Image:** an empty base with just the program in it. Goal: stay under 15 MB.
- **Details and posters:** TMDB (free key) for movies and shows, TVmaze (no key) for episode lists. No third-party metadata server.

Written from scratch. HolyArrs was used only as a reference for which features to build and how to lay out the project.

## Status

| Step | What | State |
|---|---|---|
| 1 | Skeleton: owner sign-in, Settings (qBittorrent, TMDB, folders), phone/desktop layout, Docker image | **done** |
| 2 | Indexers: sites from Prowlarr-format definition files, Torznab/Newznab feeds, search page, send to qBittorrent | **done** |
| 3 | Movies: search TMDB, add, pick the best release, send to qBittorrent, follow it, hardlink into the library | **done** |
| 4 | Shows: TMDB + TVmaze episodes, follow choices, season packs, multi-file import | **done** |
| 5 | Scheduled searches, quality upgrades, Jellyfin library refresh | **done** |
| 6 | Replace Radarr/Sonarr/Prowlarr in `arr-stack` | **done** |

## Layout

```
oneforarr/
  go.mod
  Makefile
  deploy/Dockerfile
  app/
    backend/
      cmd/oneforarr/main.go      start-up and the list of routes
      internal/
        core/config/             port and config folder (environment variables)
        core/store/              all saved data, one JSON file, safe writes
        core/webserver/          JSON helpers, security headers, serving the UI
        accounts/                owner account, sign-in, sessions
        settings/                Settings page API and Test buttons
        core/model/              shared types: search query, release, Newznab categories
        core/releasename/        reads release names: title, year, 1080p, Blu-ray...
        downloads/               qBittorrent client
        indexers/                saved indexers, search all at once, send to qBittorrent
          cardigann/             reads Prowlarr-format definition files and searches the site
          torznab/               Torznab/Newznab feeds
        library/                 shared by Movies and Shows: quality presets, release checks, placing files
        movies/                  library, release decisions, sending, following downloads, import
        shows/                   the same for TV: episodes, season packs, multi-file import
        metadata/                TMDB (movies, shows) and TVmaze (episodes)
        schedule/                the automatic work, every 30 seconds
        scan/                    the Library page: reads the Movies and TV folders
        discover/                the Discover page: trending, new and popular titles
        watchdog/                restarts qBittorrent when it's stuck without a connection
        jellyfin/                asks Jellyfin to scan after imports
    frontend/
      embed.go                   puts dist/ inside the Go program
      package.json, tsconfig.json
      static/                    index.html, icon
      src/
        main.tsx, App.tsx        start and page choice
        app.css                  all styles (phone first)
        lib/api.ts               calls to the backend, shared types
        lib/router.ts            tiny router (no library)
        components/              Layout (sidebar / bottom tabs), Icons
        routes/                  one file per page
```

## Build and run on the Mac

Needs Go 1.26 or newer and Node.js.

```
cd ~/homey/oneforarr
make test        # Go tests
make run         # builds everything, starts on http://localhost:7979, data in ./config
```

Open http://localhost:7979 and **create the owner account straight away**. Until it exists, whoever opens the app first becomes the owner.

While working on the front end, `npm --prefix app/frontend run watch` rebuilds `app.js` on every save; rebuild the Go program to pick it up (`go run ./app/backend/cmd/oneforarr` with `ONEFORARR_CONFIG_DIR=./config`).

### About the front-end build

You write only React and TypeScript. Two tools turn that into what the browser loads:

- `tsc --noEmit` checks the types (TypeScript itself).
- `esbuild` joins the files with React into one `app.js`. It's a single command in `package.json`, with no config file. A browser can't run `.tsx` files or load React's npm files directly, so something has to do this step. esbuild is written in Go.

## Indexers

Sites are described by **definition files**, the same YAML files Prowlarr uses (one `.yml` per site). The app reads them from the definitions folder (`/config/definitions` in Docker, `./config/definitions` with `make run`). Prowlarr keeps its copies in `arr-stack/config/prowlarr/Definitions/`, so for a quick start:

```
make run DEFS=../home_server/server/arr-stack/config/prowlarr/Definitions
```

Then **Indexers → Add**, pick a site, press **Test**, then **Add**. The **Search** button on the Indexers page searches every indexer that's switched on and can send a result to qBittorrent.

Supported for now: **public sites without a login** that answer in JSON or HTML (UTF-8). That covers YTS, LimeTorrents and Internet Archive. Not yet supported (they're listed but can't be picked): sites needing an account, a login or cookies, sites behind Cloudflare's browser check, and non-UTF-8 sites. A few definitions use regular-expression features Go doesn't have; those filters are skipped and logged once.

**Torznab/Newznab feeds** (from Jackett, or a site's own feed) work too: an address and an API key.

When a result only links to a details page (LimeTorrents), the app opens that page on the same site to find the magnet or `.torrent` link. qBittorrent fetches `.torrent` files itself, through the VPN. Results go in the category set in Settings.

## Movies

**Movies → Add**: search TMDB, pick the movie, choose a quality, and (optionally) search right away. If the movie's folder (`movies/Title (Year)/`) already holds a video file, it counts as downloaded.

**How a release is picked.** Each indexer result's name is read for title, year, resolution and source. A release is rejected when:

- the title or year is different (an IMDb id from the indexer overrides this), or it looks like a TV episode;
- it was recorded in a cinema (CAM, TS, TC, screener) or is 3D;
- its resolution isn't in the movie's quality (1080p: 720p–1080p; 4K: 1080p–2160p; 720p: SD–720p; Any), or the quality can't be read;
- it's bigger than the size limit (Settings → Movies, default 20 GB), smaller than 100 MB, or has fewer seeders than the minimum (default 1).

Of the rest, the best is the preferred resolution, then 5 or more seeders, then the better source (Remux > Blu-ray > WEB-DL > WEBRip > HDTV > DVD), then more seeders. **Find releases** on a movie's page lists every result with its reasons, and can send any of them by hand.

Once a movie is in the library, its page shows the file, the wanted quality (**Change** to pick another; a higher one makes the app look for an upgrade), **Look for Upgrade** (only when upgrades are on and the file is below the wanted quality), and **Replace file…**. Next to the description, every movie has **Remove From Library**, which becomes **Cancel Download** while a download is in progress. **Replace file…** lists releases as if there were no file yet, so same-quality copies are offered too (for a wrong language or a bad encode); **Use this one** takes two taps. For an upgrade or a replacement, the current file stays until the new one is downloaded and in place; then the old file is deleted (also a loose file found by the library scan) and Jellyfin scans.

**Downloading and importing.** The release goes to qBittorrent in the app's category with the tag `oneforarr-movie-<TMDB id>`. Every 30 seconds the app asks qBittorrent how its downloads are doing (Activity page). When one finishes, the largest video file is **hardlinked** to `movies/Title (Year)/Title (Year) Bluray-1080p.mkv`: no extra space, and the torrent keeps seeding. If the downloads and movies folders are on different drives, the file is copied instead (logged).

**Path mapping.** qBittorrent reports paths as it sees them (`/data/downloads/...`). When the app sees the drive elsewhere, Settings → Path mapping says how to translate. In Docker both are `/data`.

## Shows

**Shows → Add**: search TMDB, then choose a quality and what to follow:

| Follow | Means |
|---|---|
| All episodes | every episode, old and new |
| Future episodes only | episodes that haven't aired yet |
| Latest season and new ones | the newest season now, and every season after it |
| Nothing | nothing until you tick episodes or seasons |

The episode list comes from **TVmaze**, found by the show's TVDB or IMDb id from TMDB, and is updated once a day (or with **Update episode list**). New episodes are followed according to the choice above. Episodes already in the show's folder count as downloaded.

**Searching.** **Search missing** looks for every followed episode that has aired and has no file. When a whole season has aired and all of it is missing, a **season pack** is tried first, then single episodes (at most 20 single searches per press, to go easy on the indexers). Each season has **Download Season** (follows every episode in the season, then sends the best season pack, or else each episode you don't have yet), and each episode has **Search** and **Releases** (every release with the reason it was accepted or not; from there, **See Season N packs** lists the packs to pick one by hand). On the show page, each season's **Follow All Episodes** checkbox ticks or clears its episodes; downloads show inside the episode rows (a season pack at the top of its season) with their progress and **Cancel**. New shows follow nothing until you choose. Release checks are the Movies ones, plus: the show name (a country after it, like "The Office US", is fine), the season, and the episode must match; a season pack is rejected when looking for one episode and the other way round. The size limit is per episode (Settings → Shows, default 4 GB); a season pack may be that times its number of episodes.

**Importing.** Downloads are tagged `oneforarr-show-<TMDB id>-s01` (season) or `-s01e02` (episode). When one finishes, each video file whose name says which episode it is (or, for a single file, the torrent's name) is hardlinked to `tv/Show (Year)/Season 01/Show (Year) - S01E02 - Episode Title WEBDL-1080p.mkv`. Files that don't belong to what was downloaded are left alone. Files holding two episodes (S01E01E02) aren't supported yet.

## Discover

The **Discover** tab (the first tab, and the page the app opens on) shows shelves like a streaming app: trending movies and shows this week, then **New on …** and **Popular on …** for the 8 biggest streaming services in your country (Amazon's and Apple's rows come last) (Settings → Movie and show details → Country, default US). Titles you already have downloaded are left out; ones you added but haven't downloaded yet are marked **In app**. Tap a poster for its summary, rating and an **Add and search** button.

Everything comes from TMDB with the same key (TMDB's streaming data comes from JustWatch, credited on the page). The lists are fetched from TMDB again only every 6 hours or when you press **Refresh**; in between they come from a saved copy (`discover-cache.json` next to the data file, so restarts and updates don't fetch them again), and the browser shows the last page at once.

## Library scan (files already on the drive)

Movies or Shows → **Scan folders** opens the Library page; **Scan folders** there reads the Movies and TV folders. It runs only when you press it.

- A known movie or episode whose file is gone is forgotten (it becomes wanted again if it's followed).
- A video file dropped into a known movie's folder, or into a known show's folder with SxxEyy in its name, is picked up.
- A folder (or, in Movies, a loose video file) that no movie or show uses is looked up on TMDB by its name, e.g. `Alien (1979)`, `Alien (1979) {tmdb-348}` or `Alien.1979.1080p.BluRay.x264-GRP.mkv`. It's added on its own only when TMDB has exactly one with that exact title (and year, when the name has one). The folder keeps its name; later upgrades go into it.
- Everything else is listed under **Needs a match**: search, press **This one**, or **Skip** to leave it alone in later scans (Skipped → Undo brings it back).
- Shows added this way follow **no** episodes, so nothing is downloaded for them until you switch episodes on. Movies use the default quality from Settings and are upgraded like any other when upgrades are on.
- If a folder can't be read or is empty (drive not connected), that half of the scan is skipped, so nothing is forgotten by mistake.

## Safety nets

- **Free space.** Settings → Folders → **Keep this much free on the drive** (default 50 GB; 0 = off). Nothing is sent to qBittorrent when the release would leave less than that free, and the search says so. Settings shows how much is free now.
- **Dead downloads.** Indexers sometimes list old releases with seeders that are gone. A download with no seeders and no progress for 6 hours (Settings → qBittorrent → **Give up on a download with no seeders after**; 0 = never) is removed from qBittorrent with its partial files, its release is never picked again, and the app searches for another. Queued downloads don't count. Activity shows "No seeders and no progress for …" after half an hour. Also raise qBittorrent's **Maximum active downloads** (Options → BitTorrent → Torrent Queueing) and tick **Do not count slow torrents in these limits**, so dead torrents don't hold the download slots.
- **Cancel.** Every download has **Cancel Download** (tap twice) on Activity and on the movie's page (**Cancel** in a show's episode row). It removes the torrent and its partial files. For a movie, that release is never picked again but the movie stays wanted (remove the movie to stop it). For a show, the episodes it was for stop being followed, so they aren't downloaded again on their own.
- **qBittorrent watchdog.** After a VPN reconnect qBittorrent can be left with 0 DHT nodes and downloads stuck at "Downloading metadata". When qBittorrent is disconnected, or has 0 DHT nodes while the app's downloads aren't moving, for 5 minutes, the app asks qBittorrent to quit; the linuxserver image starts it again by itself. At most one restart per 15 minutes. Turn it off in Settings → qBittorrent; **Restart qBittorrent now** does it by hand. The Activity page shows the connection (DHT nodes, last restart). If qBittorrent can't be reached at all, the VPN container itself probably restarted: run `docker compose restart qbittorrent`.
- **Logs.** `docker logs oneforarr` (or the terminal with `make run`) has a line for every search result, every release sent (with the indexer and the kind of link: torrent file, magnet, or magnet without trackers), every import, and every failed request with its reason. The Activity page shows the indexer and link kind too.

## Automatic work

Every 30 seconds the app follows its downloads and imports finished ones. On top of that (Settings → Automatic, 0 turns one off):

| What | Default | How |
|---|---|---|
| Check the newest releases | every 60 minutes | One empty search per indexer (its newest releases). Any that a wanted movie or episode needs, and that passes the usual checks, is sent. Season packs are only taken for a fully aired season with nothing downloaded yet. |
| Search for each wanted item | every 12 hours | One movie and one show at a time, the one that waited longest first, so indexers aren't flooded. A show is also searched soon after a new episode airs. |

**Wanted** means: followed, aired (for episodes), not downloading, and either no file yet or, with **Upgrade when a better release appears** on, a file below the item's quality (for example a 720p file when the item wants 1080p). An upgrade replaces the old file once the new one is imported. Upgrades stop at the wanted quality, so nothing is downloaded twice at the same resolution.

**Jellyfin.** With an address and API key in Settings → Jellyfin, the app asks Jellyfin to scan its libraries after files are imported (at most once a minute). Make a key in Jellyfin: Dashboard → API Keys → +.

### Settings when trying it on the Mac (`make run`)

| Setting | Value |
|---|---|
| qBittorrent address | `http://localhost:8080` |
| Jellyfin address | `http://localhost:8096` |
| Folders: Movies / Shows / Downloads | `/Volumes/MyDrive/Media/movies`, `.../tv`, `.../downloads` |
| Path mapping | qBittorrent: `/data`, this app: `/Volumes/MyDrive/Media` |

When moving into Docker, see "Moving from `make run` into Docker" below.

## Docker

```
make size        # builds oneforarr:dev and prints its size
```

The image has no shell. It checks itself for Docker's health check (`/oneforarr healthcheck`).

It runs in the download stack, `~/homey/home_server/server/arr-stack/docker-compose.yml`, which builds it from this folder:

```yaml
  oneforarr:
    build:
      context: ../../../oneforarr
      dockerfile: deploy/Dockerfile
    image: oneforarr:local
    container_name: oneforarr
    user: "${PUID}:${PGID}"       # files it creates belong to you
    ports:
      - 7979:7979
    volumes:
      - ./config/oneforarr:/config   # its data file, plus definitions/ (indexer .yml files)
      - ${MEDIA_DIR}:/data
    restart: unless-stopped
```

It runs **outside** the VPN; qBittorrent stays inside gluetun, so in Settings its address is `http://gluetun:8080`. Jellyfin runs on the Mac itself, so its address is `http://host.docker.internal:8096`.

After changing the code: `docker compose up -d --build oneforarr` (from the `arr-stack` folder).

### Moving from `make run` into Docker

Run these from `~/homey/home_server/server/arr-stack`. Stop `make run` first (Ctrl-C).

```
mkdir -p config/oneforarr
cp ~/homey/oneforarr/config/oneforarr.json config/oneforarr/
cp -R config/prowlarr/Definitions config/oneforarr/definitions
docker compose build oneforarr
docker compose run --rm oneforarr repath /Volumes/MyDrive/Media /data
docker compose up -d --remove-orphans
```

- `repath` changes the saved folders and the path of every imported movie and episode from the Mac's path to `/data` (use your own media folder as the first path). It prints how many paths changed.
- `--remove-orphans` stops and removes the old Radarr, Sonarr and Prowlarr containers. Their settings stay in `config/radarr`, `config/sonarr` and `config/prowlarr`; delete those folders once you no longer need them.

Then open http://localhost:7979 → Settings, change the qBittorrent address to `http://gluetun:8080` and the Jellyfin address to `http://host.docker.internal:8096`, press **Test** on both, and **Save**. Downloads that were in progress keep going and are imported as usual.

## Settings

| Setting | Value for this setup |
|---|---|
| qBittorrent address | `http://gluetun:8080` |
| qBittorrent username / password | the ones from qBittorrent's Web UI settings |
| Category | `oneforarr` (the app only touches torrents with this category) |
| TMDB key | free: TMDB account → Settings → API. The short "API key" or the long "read access token" both work |
| Folders | `/data/movies`, `/data/tv`, `/data/downloads` (all on one drive, so moves are instant) |
| Jellyfin address / API key | `http://host.docker.internal:8096` (Jellyfin runs on the Mac, outside Docker); key from Jellyfin: Dashboard → API Keys |

qBittorrent, TMDB and Jellyfin each have a **Test** button that checks what's typed before saving.

## Security

- One owner account. Passwords are stored as PBKDF2-SHA256 hashes; sessions last 30 days and are stored only as hashes.
- 10 failed sign-ins from one address locks that address out for 15 minutes.
- Changes must be sent as JSON with the session cookie (`SameSite=Lax`), so other websites can't act for you.
- `/config/oneforarr.json` holds the qBittorrent password and TMDB key in plain text, readable only by the app's user (mode 600). Keep the config folder private.
- Don't forward port 7979 on the router. From the phone, use Tailscale.

## Credits

Movie and show details and images come from [TMDB](https://www.themoviedb.org). This product uses the TMDB API but is not endorsed or certified by TMDB. Episode information comes from [TVmaze](https://www.tvmaze.com), licensed CC BY-SA.
