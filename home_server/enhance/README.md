# AI picture enhancer

Turns one live broadcast into a cleaner, sharper stream at twice the size:
720p becomes 1440p, and 1080i becomes 4K. `ota_upscaler.py` starts it automatically
for those channels (one loaded worker per format); nothing here needs to be run by hand.

```
broadcast (MPEG-TS in)
   |
   +--> cleanup_stage.py   decode, then the cleanup network removes compression damage
   |        |              (Core ML on the Neural Engine; its own process)
   |        |              1080i only: each half-picture is cleaned by itself, then ffmpeg
   |        |              deinterlaces, so 59.94 full pictures a second come out either way
   |        v
   |    upscale.py         Apple's 2x AI upscaler, in overlapping tiles (VideoToolbox on the GPU)
   |        |
   |        v
   +--> ffmpeg             hardware HEVC, with the original sound (720p: light sharpening here;
            |              1080i is sharpened earlier, at 1080p, to save processor time)
            |
        enhanced stream (MPEG-TS out)
```

| File | Role |
|---|---|
| `worker.py` | Runs one stream from start to finish. Read this first. |
| `cleanup_stage.py` | Step 1, including how frames are laid out for the network. |
| `upscale.py` | Step 2. |
| `common.py` | The two formats (sizes, tiles), the output bitrate, and two small helpers shared by the above. |
| `models/look1.mlpackage` | The cleanup network for 720p frames. Trained in `bench/cleanup` (run `look1`). |
| `models/look1_1080i.mlpackage` | The same network and weights, exported for one 1080i field (1920x540). |
| `requirements.txt` | What `.venv` needs: numpy, coremltools, pyobjc. |

## Measured on the MacBook Air M4

- 720p: about 8 ms per frame for cleanup and 3.5 ms for upscaling, running side by side;
  live TV allows 16.7 ms. Capacity about 107 pictures a second; held 59.94 for 30 minutes.
- 1080i: capacity about 72 pictures a second (about 19% to spare) since finished pictures
  are written to the encoder on a second thread; it was 67 before. The 4K upscale is now the
  only limit. Not yet tested over a full game; a hot Air, or Jellyfin converting the stream
  for a TV that cannot play it directly, can push it below live speed.
- Loading takes 4 to 10 seconds, which is why a worker is always kept loaded and waiting.
- Adds one frame (17 ms) of delay. Sound and picture stay in step.

## Changing things

- **A newer cleanup network:** train it in `bench/cleanup`, export it with
  `export_coreml.py` (once per format), copy the `.mlpackage` files into `models/`,
  and pass `--model models/<name>.mlpackage` (or replace the `look1` files).
- **Sharpening and bitrate:** `worker.py --sharpness 0.4 --bitrate 40M`. The default bitrate
  is `BITRATE` in `common.py`, with the reason it must stay at 40M or less; the service only
  overrides it when started with `--enhance-bitrate`.
- **Another format or tile layout:** the table in `common.py`.
- **Testing without Apple hardware:** `ENHANCE_FAKE=1 python3 worker.py < in.ts > out.ts`
  swaps both AI steps for simple stand-ins, to check the plumbing on any computer.
