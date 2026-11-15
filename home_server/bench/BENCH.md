# The workshop (bench/)

Tools for measuring and improving the picture. Nothing here is needed to run the
server; the live enhancer is in `../enhance`.

| Tool | What it is for |
|---|---|
| `quality_bench.py` | Scores picture quality: makes a simulated broadcast from a clean clip, then compares every processed version with the clean original (`quality/`). |
| `live_chain.py` | Runs the whole chain on a recording in one pass: speed, a 30-minute heat test (`--pace --loop 5 --no-file`), and a score for test clips. |
| `cleanup/` | Training the cleanup network: `make_pairs.py` (training data) -> `train_cleanup.py` (accuracy) -> `train_look.py` (look) -> `export_coreml.py` -> copy into `../enhance/models`. `apply_cleanup.py` and `speed_cleanup.py` test a network alone. |
| `ai_upscale_bench.py` | The original benchmark of Apple's upscaling modes (described below). |
| `record_clips.py`, `fetch_sources.sh` | Recording clips from the tuner; downloading clean training footage into `sources/`. |

Environments: `.venv` (Python 3.15, for `ai_upscale_bench.py`) and `.venv-coreml`
(Python 3.12 with PyTorch and Core ML tools, for everything in `cleanup/` and `live_chain.py`).

Current best network: `cleanup/runs/look1` (trained on `cleanup/data_v3`).

---

# AI upscale benchmark

Tests Apple's AI upscalers (built into macOS) on a clip from your HDHomeRun:
the real-time model, and the slower high-quality model that also looks at the
previous frame. It can also clean up the broadcast's compression damage first.
It answers three questions:

1. Does your Mac offer it, and can it go straight to 4K?
2. Is it fast enough for live TV (59.94 frames per second)?
3. Does it look better than normal scaling on your TV?

Nothing here touches the running upscaler or Jellyfin.

## 1. One-time setup

```
cd ~/homey/home_server/bench
python3 -m venv .venv
source .venv/bin/activate
pip install pyobjc numpy
```

Homebrew's Python doesn't allow installing packages system-wide, so this keeps
them in a private folder (`.venv`). Run `source ~/homey/home_server/bench/.venv/bin/activate`
again whenever you open a new Terminal window to use the benchmark.

## 2. Check what your Mac supports

```
python3 ai_upscale_bench.py info
```

Paste the whole output to Claude. The key lines are `supported: True` and the
scale factors: `3.0` for 1280x720 means 720p can go straight to 4K, and `2.0`
for 1920x1080 means 1080 can. The last block covers the high-quality model,
including whether it's downloaded yet.

## 3. Record test clips

```
python3 record_clips.py
```

This finds a 720p channel and a 1080i channel and records 60 seconds of each
into `clip_720p.ts` and `clip_1080i.ts`. It uses the formats the upscaler has
already seen. For any it hasn't, it checks channels for a few seconds each and
remembers what it finds. Close any other streams first, since it needs a free tuner.

- A specific channel: `python3 record_clips.py --channel 2.1`
- Longer clips for the heat test: `python3 record_clips.py --seconds 300`
- If `hdhomerun.local` doesn't work: add `--hdhr <HDHomeRun IP>`

Your Mac's AI model can't take a 1080 picture whole yet, so only the 720p clip
works in steps 4 and 6 for now.

## 4. Speed test

```
python3 ai_upscale_bench.py run clip_720p.ts --no-output
```

`--no-output` skips saving video, so it measures the AI alone. It runs the AI
modes (AI 2x tiles and the high-quality model, by default) on every frame and
prints each one's time per frame every 10 seconds.
Live TV allows 16.7 ms per frame. At the end it says whether each mode is fast
enough.

## 5. Heat test (optional)

Record 5-minute clips (`python3 record_clips.py --seconds 300 --only 720p`) and run step 4 on them. If the frame rate
in the 10-second lines drops as the run goes on, the Air is heating up and
slowing down.

## 6. Picture test

```
python3 ai_upscale_bench.py run clip_720p.ts --seconds 30 --modes ai,ai2x,aiq
```

One run makes six files next to the clip:

| File | What it is |
|---|---|
| `clip_720p_original_720p.ts` | The broadcast as-is: what you watch today |
| `clip_720p_plain_4k.mp4` | Normal scaling to 4K, no AI |
| `clip_720p_ai_4k.mp4` | AI 1.5x (720p to 1080p), then stretched to 4K |
| `clip_720p_ai2x_4k.mp4` | AI 2x in two overlapping halves (720p to 1440p), then to 4K |
| `clip_720p_ai2x_sharp_4k.mp4` | Same as ai2x, plus light sharpening |
| `clip_720p_aiq_4k.mp4` | Apple's high-quality model (4x on your Air), brought to 4K |

Writing five 4K files at once runs slower than real time. That's only the
file-making; the speed lines at the end show whether each AI mode keeps up with
live TV.

To try stronger or weaker sharpening: `--sharpness 0.6` (0 to 1, default 0.4).

To watch them on the TV:

```
mkdir -p ~/Media/movies/"AI test"
cp clip_720p_original_720p.ts clip_720p_*_4k.mp4 ~/Media/movies/"AI test"/
```

Scan the Movies library in Jellyfin (Dashboard → Libraries → Scan) and play each
on the Cube. Wide shots are the real test: small players, the ball, jersey
numbers, the score bar.

## 7. Side-by-side snapshot

The quickest way to judge detail is one frame from every version, in a grid:

```
python3 ai_upscale_bench.py run clip_720p.ts --no-output --snapshot 60
```

This makes `clip_720p_compare.png`: the frame at 60 seconds, cut from the middle
of the 4K picture, with labelled panels. Every panel is the exact same frame.

| | Column 1 | Column 2 | Column 3 | Column 4 |
|---|---|---|---|---|
| Top row (broadcast as it arrives) | Plain scaling | AI 2x tiles | AI 2x tiles + sharpening | AI quality model |
| Bottom row (cleanup first) | Plain scaling | AI 2x tiles | AI 2x tiles + sharpening | AI quality model |

**Cleanup** removes the broadcast's compression damage before scaling: the
blocky squares (deblocking, using the broadcast's own compression data), fizzy
noise (light denoising), and stepped bands in smooth areas like the sky or
grass (debanding). If the bottom row looks cleaner without looking smeared,
cleanup is worth adding to the live upscaler.

**AI quality model** is Apple's slower model that uses the previous frame as
well as the current one, so it can rebuild detail that moves across frames.
On your Air it only works at 4x (720p to 5120x2880), so its result is brought
down to 4K afterwards, like the others. It also only accepts full-colour RGB
pictures, so the benchmark converts each frame for it. That makes it slow here:
expect a 15-second file to take several minutes. The
first run downloads it from Apple (one time only, with a progress count). The
run jumps to two seconds before the snapshot and runs every frame from there,
so the model has history to work with.

The run takes a minute or two. At the end it also prints:

- each AI mode's speed (rough, since it's only about 120 frames; step 4 gives
  steadier numbers)
- how fast the cleanup filters run on the CPU, each one alone and all together.
  Live TV needs 60 fps, so this shows which parts could run live.

Options:

- Pick a moment with a wide shot: change `60` to that many seconds into the clip.
- Look somewhere else in the picture: `--crop 0,0` is the top-left corner, and
  `--crop 2560,1440` is the bottom-right. The numbers are 4K pixel positions.
- Add the AI 1.5x column: `--modes ai,ai2x,aiq`
- Leave out the cleanup row: `--no-cleanup`
- Try a different cleanup: `--cleanup-filter "spp=quality=2,deband"` (any ffmpeg
  filters, separated by commas)

Open the image on the Mac and zoom in, or attach it to the chat with Claude.

## 8. Noise filter and smaller-input quality model

Two more versions are now in the snapshot and the files by default:

- **AI denoise + AI 2x tiles** (`tnr2x`): Apple's AI noise filter compares each
  frame with the previous ones to clean up broadcast noise, then the AI 2x tiles
  upscale it. It only looks backwards, so it adds no delay, and it's meant to be
  fast enough for live TV. A sharpened version is included too. Change the
  noise filter's strength with `--denoise 0.3` (0 to 1, default 0.5).
- **AI quality model 4x from 540p** (`aiq540`): the high-quality model fed a
  960x540 picture, so its 4x result is exactly 4K with nothing thrown away. It
  should be well under half the work of the full-size version. The question is
  whether starting smaller loses detail you can see.

For a quick look at the picture:

```
python3 ai_upscale_bench.py run clip_720p.ts --no-output --snapshot 60 --no-cleanup
```

For files to watch (the quality models make this slow):

```
python3 ai_upscale_bench.py run clip_720p.ts --seconds 15
```

The speed report now also shows each mode's first and last 10 seconds, so you
can see whether it slows down as the Mac warms up.

## 9. Speed-ups for the denoise + 2x mode

Two options to try, alone or together, with a breakdown of where each frame's
time goes printed at the end:

- `--parallel-tiles`: run the left and right 2x tiles at the same time.
- `--pipeline`: denoise the next frame while upscaling the current one.

```
python3 ai_upscale_bench.py run clip_720p.ts --no-output --modes tnr2x --seconds 60
python3 ai_upscale_bench.py run clip_720p.ts --no-output --modes tnr2x --seconds 60 --parallel-tiles
python3 ai_upscale_bench.py run clip_720p.ts --no-output --modes tnr2x --seconds 60 --pipeline
python3 ai_upscale_bench.py run clip_720p.ts --no-output --modes tnr2x --seconds 60 --parallel-tiles --pipeline
```

The picture is identical either way; only the speed changes.

## What to send back

The output of steps 2 and 4, what you thought of step 6, and the snapshot from
step 7 along with its printed output (speeds and cleanup speed).
