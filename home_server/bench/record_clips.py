#!/usr/bin/env python3
"""
Record test clips from the HDHomeRun for the AI upscale benchmark.

    python3 record_clips.py
        Finds a 720p channel and a 1080i channel and records 60 seconds of each:
        clip_720p.ts and clip_1080i.ts in this folder.

    python3 record_clips.py --seconds 300        5-minute clips (heat test)
    python3 record_clips.py --only 720p          just the 720p clip
    python3 record_clips.py --channel 2.1        record one specific channel

Channel formats come from the upscaler's channel_formats.json (every channel
you've tuned). If a format isn't known yet, this checks channels for a few
seconds each until it finds one, and saves what it learns to that file.

Only needs Python and ffmpeg (for ffprobe). Uses one tuner at a time.
"""

import argparse
import json
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
FORMATS_FILE = HERE.parent / "channel_formats.json"   # written by ota_upscaler.py
PROBE_SECONDS = 4
CHUNK = 188 * 1024


def lineup(hdhr):
    with urllib.request.urlopen(f"http://{hdhr}/lineup.json", timeout=10) as r:
        chans = json.load(r)
    return [c for c in chans if c.get("URL") and not c.get("DRM")]


def load_formats():
    try:
        return json.loads(FORMATS_FILE.read_text())
    except Exception:
        return {}


def save_formats(data):
    try:
        FORMATS_FILE.write_text(json.dumps(data, indent=2, sort_keys=True))
    except OSError as e:
        print(f"  (couldn't save {FORMATS_FILE.name}: {e})")


def stream_url(ch, seconds):
    return f"{ch['URL']}?duration={int(seconds)}"


def fetch(url, dest, seconds, max_bytes=None, show=False):
    """Copy the stream to dest for up to `seconds`. Returns bytes written."""
    try:
        resp = urllib.request.urlopen(url, timeout=10)
    except urllib.error.HTTPError as e:
        if e.code == 503:
            raise RuntimeError("all tuners are busy (close VLC / Jellyfin streams and retry)")
        raise RuntimeError(f"HDHomeRun returned HTTP {e.code}")
    start, got, last = time.monotonic(), 0, -1
    with resp, open(dest, "wb") as f:
        while True:
            data = resp.read1(CHUNK)
            if not data:
                break
            f.write(data)
            got += len(data)
            elapsed = time.monotonic() - start
            if show and int(elapsed) != last:
                last = int(elapsed)
                print(f"\r  recording... {last:3d}/{int(seconds)}s  {got / 1e6:6.1f} MB", end="", flush=True)
            if (max_bytes and got >= max_bytes) or elapsed > seconds + 5:
                break
    if show:
        print()
    return got


def describe(path):
    """Return {'label': '1080i', 'aspect': '16:9', ...} for a recorded file, or None."""
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
         "stream=width,height,field_order,display_aspect_ratio,codec_name,r_frame_rate",
         "-of", "json", str(path)], capture_output=True, text=True)
    try:
        s = json.loads(out.stdout)["streams"][0]
    except Exception:
        return None
    h = int(s.get("height", 0))
    if not h:
        return None
    lines = min((2160, 1080, 720, 576, 480), key=lambda n: abs(n - h))
    interlaced = s.get("field_order", "progressive") not in ("progressive", "unknown", "")
    dar = s.get("display_aspect_ratio") or "?"
    if dar in ("0:1", "N/A"):
        dar = "?"
    return {"label": f"{lines}{'i' if interlaced else 'p'}", "aspect": dar,
            "codec": s.get("codec_name", "?"), "size": f"{s.get('width')}x{h}"}


def probe_channel(ch):
    with tempfile.NamedTemporaryFile(suffix=".ts") as tmp:
        fetch(stream_url(ch, PROBE_SECONDS), tmp.name, PROBE_SECONDS, max_bytes=6_000_000)
        return describe(tmp.name)


def sort_key(ch):
    # Prefer main channels (x.1) and ones the HDHomeRun marks as HD.
    num = str(ch["GuideNumber"])
    sub = num.split(".")[1] if "." in num else "1"
    return (0 if ch.get("HD") else 1, 0 if sub == "1" else 1,
            [int(x) if x.isdigit() else 0 for x in re.split(r"[.-]", num)])


def find_channel(chans, label, formats):
    known = [c for c in chans if formats.get(str(c["GuideNumber"]), {}).get("label") == label
             and formats[str(c["GuideNumber"])].get("aspect") in ("16:9", None)]
    if known:
        return sorted(known, key=sort_key)[0], "from channel_formats.json"
    unknown = [c for c in sorted(chans, key=sort_key) if str(c["GuideNumber"]) not in formats]
    if unknown:
        print(f"  No {label} channel known yet; checking channels ({PROBE_SECONDS}s each)...")
    for ch in unknown:
        num = str(ch["GuideNumber"])
        try:
            fmt = probe_channel(ch)
        except RuntimeError as e:
            sys.exit(f"  {num}: {e}")
        if not fmt:
            print(f"    {num} {ch.get('GuideName', '')}: no picture (weak signal?)")
            continue
        formats[num] = {"label": fmt["label"], "aspect": fmt["aspect"]}
        save_formats(formats)
        print(f"    {num} {ch.get('GuideName', '')}: {fmt['label']} {fmt['aspect']}")
        if fmt["label"] == label and fmt["aspect"] in ("16:9", "?"):
            return ch, "found by checking"
    return None, None


def record(ch, out, seconds):
    num, name = str(ch["GuideNumber"]), ch.get("GuideName", "")
    print(f"Recording {num} {name} for {int(seconds)}s -> {out.name}")
    got = fetch(stream_url(ch, seconds), out, seconds, show=True)
    fmt = describe(out)
    if not fmt or got < 100_000:
        print(f"  Recording looks empty ({got} bytes). Check the antenna signal on {num}.")
        return False
    print(f"  Saved {got / 1e6:.0f} MB: {fmt['label']} {fmt['aspect']} {fmt['codec']} ({fmt['size']})")
    return True


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--hdhr", default="hdhomerun.local", help="HDHomeRun IP or name (default hdhomerun.local)")
    p.add_argument("--seconds", type=float, default=60, help="clip length (default 60)")
    p.add_argument("--only", choices=("720p", "1080i"), help="record just one kind")
    p.add_argument("--channel", help="record this channel number instead of searching")
    p.add_argument("--out", default=str(HERE), help="folder for the clips (default: this folder)")
    args = p.parse_args()

    if not shutil.which("ffprobe"):
        sys.exit("ffprobe not found. Install it with: brew install ffmpeg")
    try:
        chans = lineup(args.hdhr)
    except Exception as e:
        sys.exit(f"Can't reach the HDHomeRun at {args.hdhr} ({e}). Try --hdhr <its IP>.")
    out_dir = Path(args.out).expanduser()
    out_dir.mkdir(parents=True, exist_ok=True)

    try:
        if args.channel:
            ch = next((c for c in chans if str(c["GuideNumber"]) == args.channel), None)
            if not ch:
                sys.exit(f"Channel {args.channel} isn't on the HDHomeRun's list.")
            ok = record(ch, out_dir / f"clip_{args.channel}.ts", args.seconds)
            sys.exit(0 if ok else 1)

        formats, made = load_formats(), []
        for label in ([args.only] if args.only else ["720p", "1080i"]):
            ch, how = find_channel(chans, label, formats)
            if not ch:
                print(f"No {label} channel found on this antenna; skipping.")
                continue
            print(f"{label}: using {ch['GuideNumber']} {ch.get('GuideName', '')} ({how})")
            out = out_dir / f"clip_{label}.ts"
            if record(ch, out, args.seconds):
                made.append(out.name)
    except RuntimeError as e:
        sys.exit(f"Stopped: {e}")
    except KeyboardInterrupt:
        sys.exit("\nStopped.")

    if made:
        print("\nNext:")
        for name in made:
            print(f"  python3 ai_upscale_bench.py run {name} --no-output")


if __name__ == "__main__":
    main()
