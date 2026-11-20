# homey

A home server on a MacBook Air M4 (a Mac mini later). Its main job: take live
over-the-air TV from an antenna, make it look like a clean high-quality stream with AI
in real time, and serve it through Jellyfin to a Fire TV Cube on a 4K TV.

```
antenna -> HDHomeRun tuner -> ota_upscaler.py (+ the AI enhancer) -> Jellyfin -> Fire TV
```

- 720p channels (FOX, ABC): cleanup network, then Apple's AI 2x upscale to 1440p.
- 1080i channels (CBS, NBC): cleanup network on each field, deinterlace, AI 2x upscale to 4K.
- Everything else: deinterlaced and scaled to 1080p.

Everything lives in [`home_server/`](home_server/):

| Folder | What it is | Start reading |
|---|---|---|
| `home_server/` | The service between the tuner and Jellyfin (Python standard library only) | [`README.md`](home_server/README.md) |
| `home_server/enhance/` | The AI picture enhancer the service runs for 720p and 1080i channels | [`README.md`](home_server/enhance/README.md) |
| `home_server/bench/` | The workshop: training, scoring and timing tools. Not needed to run the server | [`BENCH.md`](home_server/bench/BENCH.md) |

Downloading movies and shows is handled by [`oneforarr/`](oneforarr/README.md), our own
replacement for Radarr, Sonarr and Prowlarr (Go + React). The download stack in
`home_server/server/arr-stack/` builds and runs it.
| `home_server/server/` | The on/off script and the download stack (qBittorrent + OneForArr) | [`SETUP.md`](home_server/server/SETUP.md) |

## Not in this repository

These stay on the Mac (see [`.gitignore`](.gitignore)):

- **Secrets:** `server/server.conf` (Jellyfin API key) and `server/arr-stack/.env`
  (VPN key). Copy the `.example` files next to them and fill them in.
- **Python environments.** To recreate them:
  - `home_server/enhance/.venv` (the enhancer): see "Set it up once" in `home_server/README.md`.
  - `home_server/bench/.venv` (`ai_upscale_bench.py` only): see `home_server/bench/BENCH.md`.
  - `home_server/bench/.venv-coreml` (training, scoring, timing), Python 3.12:
    ```
    cd ~/homey/home_server/bench
    /opt/homebrew/opt/python@3.12/bin/python3.12 -m venv .venv-coreml
    .venv-coreml/bin/pip install torch torchvision coremltools pyobjc numpy
    ```
    (Built with torch 2.15 and coremltools 9.)
- **Video and training data:** TV recordings, downloaded footage, test outputs and
  training patches (tens of GB, and mostly material that must not be published).
  `bench/fetch_sources.sh` downloads the public test footage again,
  `bench/record_clips.py` records from the tuner, and `bench/cleanup/make_pairs.py`
  rebuilds the training data.
- **The VGG-19 weights** used for look training, downloaded automatically on first use.

The trained cleanup networks the server runs are included
(`home_server/enhance/models/`), along with the training checkpoints and logs in
`home_server/bench/cleanup/runs/`.

## Where it lives on the Mac

The working copy is `~/homey/home_server`. All paths in the docs, scripts and startup
files use that location.
