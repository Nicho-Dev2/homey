# OTA upscaler

Sits between your HDHomeRun and Jellyfin. When you tune a channel, it deinterlaces
the broadcast and scales it to 1080p at 59.94fps using the Mac's hardware encoder,
then hands it to Jellyfin.

| Broadcast | What you get |
|---|---|
| 1080i | Deinterlaced to 1080p60 |
| 720p | Scaled to 1080p60 |
| 480i SD (4:3) | Deinterlaced, scaled to 1440x1080, black bars on the sides |

The original Dolby (AC-3) audio, including second-language tracks, passes through
untouched. Encrypted ATSC 3.0 channels are left out of the channel list.

### How each channel is processed

The upscaler remembers each channel's format (in `channel_formats.json`) and picks
a method the next time you tune it:

| Channel format | Typical networks | Default (`auto`) |
|---|---|---|
| 720p | FOX, ABC | **AI picture enhancer** when `enhance/` is set up (see below); otherwise scaled on the M4's video hardware |
| 1080i | CBS, NBC | **AI picture enhancer** when `enhance/` is set up; otherwise deinterlaced on the processor with `bwdif`, the cleanest option for fast motion |
| SD / 4:3 | Subchannels | Processor (needs the black side bars) |
| Not seen before | Any | Processor, then remembered for next time |

The log shows which method each stream used, for example
`... -> output 1080p 59.94fps H.264 [video hardware scaling]`. If a hardware step
ever fails, that channel switches to the processor straight away, and the log
explains why.

### The AI picture enhancer (720p and 1080i channels)

Broadcasts are heavily compressed, and plain scaling can't hide that. When the
`enhance/` folder is set up, 720p and 1080i channels take a different route:

```
broadcast 720p  -> cleanup network (removes compression damage, Neural Engine)
                -> AI 2x upscale to 1440p (GPU) -> sharpen -> HEVC at 40 Mbps

broadcast 1080i -> cleanup network on each half-picture -> deinterlace -> sharpen
                -> AI 2x upscale to 4K (GPU) -> HEVC at 40 Mbps
```

- It is automatic. If `enhance/.venv` exists, the upscaler loads the enhancer when it
  starts and the log says `AI enhancer loaded and waiting for a 720p channel`, and the
  same for 1080i.
- One enhanced stream at a time. A second one at the same moment gets the standard
  processing, as does any channel tuned in the few seconds while the enhancer loads.
- If the enhancer ever fails, the channel falls back to standard processing and the log says why.
  Only that format (720p or 1080i) is switched off, until the upscaler restarts.
- 1080i to 4K has about 19% speed to spare on the MacBook Air. If it ever falls behind live
  TV, the log says `enhancer: only NN pictures a second over the last minute`.
- Enhanced streams are HEVC at 40 Mbps. A TV that cannot play that directly (the Roku TV,
  Nov 14) makes Jellyfin convert it on this Mac, which costs speed and some of the picture.
  The Fire TV Cube plays it directly.
- To turn it off, add `--enhance off` to the upscaler's arguments.

Set it up once (needs Python 3.12, because Apple's Core ML tools don't support newer ones yet):

```
brew install python@3.12
cd ~/homey/home_server/enhance
/opt/homebrew/opt/python@3.12/bin/python3.12 -m venv .venv
.venv/bin/pip install -r requirements.txt
```

Try it on a recording without the upscaler (plays in VLC):

```
cd ~/homey/home_server/enhance
.venv/bin/python worker.py < ../bench/clip_720p.ts > /tmp/enhanced.ts
```

### What is in this folder

| Path | What it is |
|---|---|
| `ota_upscaler.py` | The service: channel list, guide, and one video process per stream. Standard library only. |
| `enhance/` | The AI picture enhancer, a separate program the service starts for 720p and 1080i channels. |
| `enhance/worker.py` | One live stream: broadcast in, enhanced stream out. Start reading here. |
| `enhance/cleanup_stage.py` | Step 1: decode + cleanup network (Core ML). Runs as its own process. |
| `enhance/upscale.py` | Step 2: Apple's 2x AI upscaler (VideoToolbox). |
| `enhance/models/` | The trained cleanup network (`look1`). |
| `server/` | On/off script and the download stack (qBittorrent in a VPN + OneForArr). |
| `bench/` | The workshop: training, scoring and timing tools. Not needed to run the server. |

## 1. Install the tools

```
brew install python ffmpeg
```

## 2. Find your HDHomeRun's IP address

Open the HDHomeRun app or your router's device list. In your router, give it a
reserved (fixed) IP address so it never changes. Check it works by opening
`http://<that-ip>/lineup.json` in a browser — you should see your channels.

## 3. Try it by hand

The files live in `~/homey/home_server`. Start the upscaler:

```
python3 ~/homey/home_server/ota_upscaler.py --hdhr 192.168.1.50
```

(use your HDHomeRun's IP). Then:

- Open `http://localhost:8090/playlist.m3u` in a browser: you should see your channels.
- Open a channel in VLC (File → Open Network → `http://localhost:8090/stream/2.1`).
- `http://localhost:8090/status` shows what's currently streaming and when the guide last updated.
- `http://localhost:8090/guide.xml` is the TV guide Jellyfin reads.

Press Ctrl+C to stop.

## 4. Run it with the server toggle

`server/server.sh on` starts the upscaler (and Jellyfin), `server/server.sh off` stops it;
see `server/SETUP.md` for the one-click Shortcuts. It reaches the HDHomeRun through a small relay service (`server/hdhr_relay.py`,
installed once: `server/SETUP.md` step 3).

- Logs: `~/Library/Logs/ota-upscaler.log`
- It is started again if it ever stops on its own.

Why the relay: macOS does not reliably give a background Python the Local Network
permission, so the upscaler could not reach the HDHomeRun ("No route to host"),
whether it was started by launchd, Shortcuts, Terminal or SSH. System services
running as root are exempt, so only the relay talks to the HDHomeRun.


## 5. Add it to Jellyfin

Dashboard → Live TV → Tuner Devices → **+**:

- Tuner type: **M3U Tuner**
- File or URL: `http://127.0.0.1:8090/playlist.m3u`
- Simultaneous stream limit: **2** (your tuner count)

### TV guide (free)

The upscaler downloads SiliconDust's free guide for your HDHomeRun (about 2 days
ahead) and serves it at `http://127.0.0.1:8090/guide.xml`. It refreshes on its own
about once a day, and the channels already match the playlist, so there's nothing
to map.

Dashboard → Live TV → TV Guide Data Providers → **+** → **XMLTV**:

- File or URL: `http://127.0.0.1:8090/guide.xml`
- Save. Jellyfin pulls it again with its daily **Refresh Guide** task
  (Dashboard → Scheduled Tasks).

To check the guide downloaded, open `http://localhost:8090/status`: `guide.updated`
shows when, and `guide.error` explains any problem. If the HDHomeRun has no
`DeviceAuth`, open the HDHomeRun app once so the tuner registers with SiliconDust.

In the Jellyfin app on the Fire TV Cube, set the maximum streaming bitrate to the
highest option so the upscaled stream plays as-is.

## Options

| Option | Default | Notes |
|---|---|---|
| `--max-streams` | 2 | Set to your HDHomeRun's tuner count |
| `--bitrate` | 8M | Raise to 10M–12M for sports if you see blockiness |
| `--encoder` | h264_videotoolbox | `hevc_videotoolbox` gives smaller recordings |
| `--port` | 8090 | |
| `--width` / `--height` | 1920 / 1080 | |
| `--processing` | auto | `cpu`: always the processor. `hw`: also deinterlace 1080i on the video hardware (coolest; slightly less clean on fast motion) |
| `--no-guide` | off | Don't download or serve guide data |

## Things to check on first run

- **Closed captions:** captions are carried inside the broadcast video, and
  re-encoding may drop them. Turn on captions on a news channel and check.
- **Heat:** with two channels going, check the Air stays comfortable.
  Lowering `--bitrate` or using one stream at a time reduces load.
