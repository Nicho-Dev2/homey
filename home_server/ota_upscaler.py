#!/usr/bin/env python3
"""
OTA upscaler: sits between an HDHomeRun and Jellyfin.

- GET /playlist.m3u      M3U of every channel on the HDHomeRun, pointing back here.
- GET /stream/<channel>  Live stream of that channel, deinterlaced and scaled to
                         1080p60 with ffmpeg (hardware-encoded via VideoToolbox).
                         720p and 1080i channels go through the AI picture enhancer
                         instead (cleanup + 2x upscale to 1440p / 4K) when enhance/ is set up.
- GET /guide.xml         TV guide (XMLTV) from SiliconDust's free HDHomeRun guide,
                         with channel ids matching the playlist. Refreshed daily.
- GET /status            JSON list of active streams.

Jellyfin adds http://<this-mac>:8090/playlist.m3u as an "M3U Tuner".
This file uses only the Python standard library and needs ffmpeg on PATH.
The AI enhancer is a separate program in enhance/ with its own Python environment;
without it, everything here still works and those channels are simply scaled.
"""

import argparse
import copy
import gzip
import json
import pathlib
import random
import re
import logging
import os
import shutil
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import quote, unquote, urlparse

CHUNK = 188 * 1024          # multiple of the 188-byte MPEG-TS packet size
LINEUP_TTL = 300            # seconds to cache the HDHomeRun channel list
TUNER_TIMEOUT = 10          # seconds to wait for the HDHomeRun to start a stream

log = logging.getLogger("ota-upscaler")


# --------------------------------------------------------------------------- #
# Configuration
# --------------------------------------------------------------------------- #

def parse_args(argv=None):
    p = argparse.ArgumentParser(description="Deinterlace/upscale HDHomeRun channels for Jellyfin.")
    p.add_argument("--hdhr", required=True,
                   help="HDHomeRun IP address or hostname, e.g. 192.168.1.50")
    p.add_argument("--bind", default="0.0.0.0", help="Address to listen on (default all)")
    p.add_argument("--port", type=int, default=8090, help="Port to listen on (default 8090)")
    p.add_argument("--public-host", default=None,
                   help="Host name/IP written into the playlist URLs "
                        "(default: whatever host Jellyfin used to fetch the playlist)")
    p.add_argument("--max-streams", type=int, default=2,
                   help="Max simultaneous streams; set to your tuner count (default 2)")
    p.add_argument("--width", type=int, default=1920)
    p.add_argument("--height", type=int, default=1080)
    p.add_argument("--fps", default="60000/1001",
                   help="Output frame rate (default 59.94, the US broadcast rate)")
    p.add_argument("--encoder", default="h264_videotoolbox",
                   help="ffmpeg video encoder: h264_videotoolbox (default), "
                        "hevc_videotoolbox, or libx264 for software encoding")
    p.add_argument("--bitrate", default="8M", help="Video bitrate (default 8M)")
    p.add_argument("--keep-captions", action="store_true",
                   help="Try to carry closed captions into the new video. Off by default "
                        "for VideoToolbox, whose caption handling crashes on many broadcasts")
    p.add_argument("--processing", choices=("auto", "cpu", "hw"), default="auto",
                   help="auto (default): 720p channels are scaled on the Mac's video hardware, "
                        "1080i keeps the higher-quality processor deinterlacer. "
                        "cpu: everything on the processor. "
                        "hw: also deinterlace 1080i on the video hardware (coolest, "
                        "slightly less clean on fast motion)")
    p.add_argument("--enhance", choices=("auto", "off"), default="auto",
                   help="auto (default): 720p and 1080i channels use the AI picture enhancer when enhance/ "
                        "is set up, and standard scaling otherwise. off: never use it")
    p.add_argument("--enhance-bitrate", default=None,
                   help="Video bitrate of enhanced (1440p or 4K HEVC) streams (default: the enhancer's own, "
                        "set in enhance/common.py; read the note there before raising it)")
    p.add_argument("--enhance-python", default=None,
                   help="Python that runs the enhancer (default: enhance/.venv/bin/python)")
    p.add_argument("--test-pipeline", default=None, help=argparse.SUPPRESS)
    p.add_argument("--no-guide", action="store_true",
                   help="Don't download or serve guide data")
    p.add_argument("--guide-api", default="https://api.hdhomerun.com/api/xmltv",
                   help=argparse.SUPPRESS)
    p.add_argument("--ffmpeg", default=shutil.which("ffmpeg") or "ffmpeg",
                   help="Path to ffmpeg")
    p.add_argument("--log-level", default="INFO")
    return p.parse_args(argv)


# --------------------------------------------------------------------------- #
# HDHomeRun lineup
# --------------------------------------------------------------------------- #

class Lineup:
    """Caches the HDHomeRun's lineup.json."""

    def __init__(self, hdhr):
        self.url = f"http://{hdhr}/lineup.json"
        self._channels = {}
        self._order = []
        self._fetched = 0.0
        self._lock = threading.Lock()

    def _refresh(self):
        with urllib.request.urlopen(self.url, timeout=10) as r:
            data = json.load(r)
        channels, order = {}, []
        for ch in data:
            if ch.get("DRM") or not ch.get("URL"):
                continue  # encrypted ATSC 3.0 channels can't be played
            num = str(ch["GuideNumber"])
            channels[num] = ch
            order.append(num)
        self._channels, self._order, self._fetched = channels, order, time.time()
        log.info("Loaded %d channels from %s", len(order), self.url)

    def channels(self):
        with self._lock:
            if time.time() - self._fetched > LINEUP_TTL or not self._order:
                try:
                    self._refresh()
                except Exception as e:  # keep serving the old list if the tuner hiccups
                    if not self._order:
                        raise
                    log.warning("Lineup refresh failed, using cached list: %s", e)
            return [self._channels[n] for n in self._order]

    def get(self, number):
        for ch in self.channels():
            if str(ch["GuideNumber"]) == number:
                return ch
        return None


# --------------------------------------------------------------------------- #
# Guide data (XMLTV)
# --------------------------------------------------------------------------- #

class Guide:
    """
    Downloads SiliconDust's XMLTV guide for this HDHomeRun (free tier covers
    about 2 days), renames channels to their channel numbers so they match the
    playlist, and refreshes every 20-28 hours (SiliconDust asks for a random
    time rather than a fixed daily one).
    """

    def __init__(self, hdhr, api, lineup):
        self.hdhr, self.api, self.lineup = hdhr, api, lineup
        self.xml = None          # bytes of the rewritten guide
        self.logos = {}          # channel number -> logo URL
        self.updated = None
        self.error = None
        self._lock = threading.Lock()

    def start(self):
        threading.Thread(target=self._loop, daemon=True).start()

    def _loop(self):
        while True:
            ok = self.refresh()
            hours = random.uniform(20, 28) if ok else 1
            time.sleep(hours * 3600)

    def refresh(self):
        try:
            with urllib.request.urlopen(f"http://{self.hdhr}/discover.json", timeout=10) as r:
                auth = json.load(r).get("DeviceAuth")
            if not auth:
                raise RuntimeError("HDHomeRun has no DeviceAuth yet. Open the HDHomeRun app once "
                                   "so the tuner syncs with SiliconDust's guide server.")
            req = urllib.request.Request(f"{self.api}?DeviceAuth={auth}",
                                         headers={"Accept-Encoding": "gzip",
                                                  "User-Agent": "ota-upscaler/1.0"})
            with urllib.request.urlopen(req, timeout=60) as r:
                raw = r.read()
                if r.headers.get("Content-Encoding") == "gzip" or raw[:2] == b"\x1f\x8b":
                    raw = gzip.decompress(raw)
            xml, logos, matched, total = self._rewrite(raw)
            with self._lock:
                self.xml, self.logos = xml, logos
                self.updated, self.error = time.time(), None
            log.info("Guide updated: %d of %d channels matched", matched, total)
            return True
        except urllib.error.HTTPError as e:
            msg = f"SiliconDust guide server returned HTTP {e.code}"
            if e.code == 403:
                msg += " (guide not available for this tuner without an HDHomeRun DVR subscription?)"
            self._fail(msg)
        except Exception as e:
            self._fail(f"Guide update failed: {e}")
        return False

    def _fail(self, msg):
        with self._lock:
            self.error = msg
        log.warning("%s. Keeping the previous guide; retrying in 1 hour.", msg)

    def _rewrite(self, raw):
        root = ET.fromstring(raw)
        numbers = {str(ch["GuideNumber"]) for ch in self.lineup.channels()}

        # Map each guide channel id -> the playlist channel number(s) it belongs to.
        id_map, logos, channels = {}, {}, []
        for ch in root.findall("channel"):
            names = [(d.text or "").strip() for d in ch.findall("display-name")]
            found = [n for n in names if n in numbers]
            if not found:   # also accept "7.1 WXYZ" style names
                found = [n.split()[0] for n in names if n.split() and n.split()[0] in numbers]
            if not found:
                continue
            num = found[0]
            id_map.setdefault(ch.get("id"), []).append(num)
            icon = ch.find("icon")
            if icon is not None and icon.get("src"):
                logos[num] = icon.get("src")
            channels.append((num, names, icon))

        if not channels:
            raise RuntimeError("no guide channels matched the HDHomeRun's channel numbers")

        out = ET.Element("tv", root.attrib)
        seen = set()
        for num, names, icon in channels:
            if num in seen:
                continue
            seen.add(num)
            c = ET.SubElement(out, "channel", id=num)
            for n in dict.fromkeys([num] + names):   # keep order, drop duplicates
                if n:
                    ET.SubElement(c, "display-name").text = n
            if icon is not None:
                c.append(icon)
        for prog in root.findall("programme"):
            for i, num in enumerate(dict.fromkeys(id_map.get(prog.get("channel"), []))):
                el = prog if i == 0 else copy.deepcopy(prog)
                el.set("channel", num)
                out.append(el)
        return (ET.tostring(out, encoding="utf-8", xml_declaration=True), logos,
                len(seen), len(root.findall("channel")))

    def snapshot(self):
        with self._lock:
            return self.xml, self.updated, self.error


# --------------------------------------------------------------------------- #
# Remembered channel formats
# --------------------------------------------------------------------------- #

class Formats:
    """Remembers each channel's broadcast format (e.g. 720p 16:9) between runs,
    so the next tune can pick the best processing method up front."""

    def __init__(self, path):
        self.path = pathlib.Path(path)
        self._lock = threading.Lock()
        try:
            self._data = json.loads(self.path.read_text())
        except Exception:
            self._data = {}

    def get(self, number):
        with self._lock:
            return self._data.get(number)

    def update(self, number, video):
        if video["label"] == "unknown" or video["aspect"] == "?":
            return      # the first second showed no picture size (common on 1080i); keep what we knew
        fmt = {"label": video["label"], "aspect": video["aspect"]}
        with self._lock:
            if self._data.get(number) == fmt:
                return
            self._data[number] = fmt
            try:
                self.path.write_text(json.dumps(self._data, indent=2, sort_keys=True))
            except OSError as e:
                log.warning("Could not save channel formats to %s: %s", self.path, e)


# --------------------------------------------------------------------------- #
# ffmpeg
# --------------------------------------------------------------------------- #

PIPELINE_NAMES = {
    "ai": "AI cleanup + 2x upscale",
    "cpu": "processor",
    "gpu-scale": "video hardware scaling",
    "gpu-full": "video hardware deinterlace + scaling",
}


def choose_pipeline(cfg, fmt, failed, enhanced_formats=()):
    """Pick how to process a channel, from the format seen last time it was tuned."""
    if cfg.test_pipeline:
        return "cpu" if cfg.test_pipeline in failed else cfg.test_pipeline
    if cfg.processing == "cpu" or not fmt:
        return "cpu"        # unknown format (first tune) -> the method that handles anything
    label, aspect = fmt.get("label", ""), fmt.get("aspect")
    if label in enhanced_formats and aspect == "16:9":
        return "ai"         # the AI picture enhancer (see class Enhancer)
    if "videotoolbox" not in cfg.encoder:
        return "cpu"
    if label[:-1] not in ("720", "1080") or aspect != "16:9":
        return "cpu"        # SD / 4:3 needs padding, which only the processor path does
    if label.endswith("p"):
        choice = "gpu-scale"
    elif cfg.processing == "hw":
        choice = "gpu-full"
    else:
        return "cpu"        # 1080i: keep bwdif, the cleanest deinterlacer for fast motion
    return "cpu" if choice in failed else choice


def build_ffmpeg_cmd(cfg, pipeline="cpu"):
    w, h = cfg.width, cfg.height
    if pipeline == "gpu-scale":
        # Already progressive (720p): only the scaling step, done on the video hardware.
        vf = f"fps={cfg.fps},format=nv12,hwupload,scale_vt=w={w}:h={h}"
    elif pipeline == "gpu-full":
        vf = (f"format=nv12,hwupload,"
              f"yadif_videotoolbox=mode=send_field:parity=auto:deint=interlaced,"
              f"fps={cfg.fps},scale_vt=w={w}:h={h}")
    else:
        vf = None
    vf = vf or ",".join([
        # Deinterlace only frames flagged interlaced (1080i/480i); one output
        # frame per field gives smooth 60fps motion. 720p passes through.
        "bwdif=mode=send_field:parity=auto:deint=interlaced",
        # Normalize frame rate so every channel comes out the same.
        f"fps={cfg.fps}",
        # Convert non-square pixels (SD channels) to square, then fit into
        # 1920x1080 without stretching, padding with black bars if needed.
        "scale=trunc(iw*sar/2)*2:ih",
        f"scale={w}:{h}:force_original_aspect_ratio=decrease:flags=lanczos",
        f"pad={w}:{h}:(ow-iw)/2:(oh-ih)/2",
        "setsar=1",
        "format=yuv420p",
    ])
    cmd = [
        cfg.ffmpeg, "-hide_banner", "-loglevel", "level+info", "-nostats", "-nostdin",
        # Fast start: look at ~1s of input instead of ~2s before starting.
        # Broadcasts repeat their stream info every 0.1s, so this still finds the
        # video and audio tracks. Do NOT add "-fflags nobuffer" or "-flags low_delay"
        # here: on broadcast MPEG-2 they drop frames and decode frames out of order.
        "-fflags", "+genpts+discardcorrupt",
        "-probesize", "1000000", "-analyzeduration", "1000000",
    ]
    if pipeline != "cpu":
        cmd += ["-init_hw_device", "videotoolbox=vt", "-filter_hw_device", "vt"]
    cmd += [
        "-f", "mpegts", "-i", "pipe:0",
        "-map", "0:v:0", "-map", "0:a?",          # main video + every audio track (incl. SAP)
        "-vf", vf,
        "-c:v", cfg.encoder,
        "-b:v", cfg.bitrate,
        "-maxrate", cfg.bitrate, "-bufsize", cfg.bitrate,
        "-g", "60",                                # full picture every second, so players start sooner
        "-bf", "0",                                # no B-frames: encoder doesn't hold frames back
    ]
    if "videotoolbox" in cfg.encoder:
        cmd += ["-realtime", "1", "-allow_sw", "0"]
        if not cfg.keep_captions:
            # VideoToolbox fails with "Unexpected end of SEI NAL Unit" when it
            # copies broadcast (A/53) caption data into the new stream.
            cmd += ["-a53cc", "0"]
    elif cfg.encoder == "libx264":
        cmd += ["-preset", "veryfast", "-tune", "zerolatency"]
    cmd += [
        "-c:a", "copy",                            # keep broadcast Dolby audio untouched
        "-f", "mpegts", "-muxdelay", "0", "-muxpreload", "0", "-flush_packets", "1", "pipe:1",
    ]
    return cmd


# --------------------------------------------------------------------------- #
# AI picture enhancer (the programs in enhance/)
# --------------------------------------------------------------------------- #

class Enhancer:
    """
    Runs enhance/worker.py for 720p and 1080i channels. A worker takes the broadcast
    on its input and gives back an enhanced stream, exactly like the ffmpeg command
    the other channels use, so the rest of this file treats the two the same way.

    A worker is built for one broadcast format and needs several seconds to load
    its AI networks. To keep channel changes quick, one worker per format is always
    kept loaded and waiting: take() hands it to a stream and starts loading the
    next. Only one enhanced stream runs at a time (that is what the MacBook Air has
    room for); a second one at the same moment gets standard scaling.
    """

    FOLDER = pathlib.Path(__file__).with_name("enhance")
    OUTPUT_HEIGHT = {"720p": 1440, "1080i": 2160}     # broadcast format -> the enhanced picture (2x)

    def __init__(self, cfg):
        self.cfg = cfg
        self.python = pathlib.Path(cfg.enhance_python or self.FOLDER / ".venv" / "bin" / "python")
        installed = (cfg.enhance == "auto" and self.python.exists()
                     and (self.FOLDER / "worker.py").exists())
        self.formats = set(self.OUTPUT_HEIGHT) if installed else set()   # what can be enhanced right now
        self._waiting = {}          # format -> its loaded (or loading) worker, not yet used
        self._ready = set()         # formats whose waiting worker has finished loading
        self._busy = False          # is an enhanced stream running right now?
        self._lock = threading.Lock()

    def start(self):
        for fmt in sorted(self.formats):
            self._load_next(fmt)

    def take(self, fmt):
        """The waiting worker for this format, or None when it is still loading or a stream
        is already being enhanced."""
        with self._lock:
            if self._busy or fmt not in self._ready:
                return None
            worker = self._waiting.pop(fmt)
            self._ready.discard(fmt)
            self._busy = True
        self._load_next(fmt)
        return worker

    def release(self):
        """Call when an enhanced stream has ended."""
        with self._lock:
            self._busy = False

    def stop(self):
        with self._lock:
            workers, self._waiting = list(self._waiting.values()), {}
        for worker in workers:
            stop_process(worker)

    def _load_next(self, fmt):
        bitrate = ["--bitrate", self.cfg.enhance_bitrate] if self.cfg.enhance_bitrate else []
        worker = subprocess.Popen(
            [str(self.python), str(self.FOLDER / "worker.py"), "--format", fmt, "--ffmpeg", self.cfg.ffmpeg, *bitrate],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0,
            start_new_session=True)      # its own process group, so its helpers can be stopped with it
        worker.has_helpers = True
        with self._lock:
            self._waiting[fmt] = worker
            self._ready.discard(fmt)
        threading.Thread(target=self._wait_until_loaded, args=(worker, fmt), daemon=True).start()

    def _wait_until_loaded(self, worker, fmt):
        for raw in iter(worker.stderr.readline, b""):
            line = raw.decode(errors="replace").strip()
            if line == "READY":
                with self._lock:
                    if self._waiting.get(fmt) is worker:
                        self._ready.add(fmt)
                log.info("AI enhancer loaded and waiting for a %s channel", fmt)
                return
            if line:
                log.warning("AI enhancer (%s): %s", fmt, line)
        if self._waiting.get(fmt) is worker:          # (a worker stopped on purpose is not a failure)
            self.give_up(fmt)
            log.warning("The AI enhancer could not start for %s channels (see the lines above). They use "
                        "standard processing until the upscaler restarts.", fmt)

    def give_up(self, fmt):
        """Stop enhancing this format until the upscaler restarts; its channels get standard processing.
        The one rule for both ways the enhancer can fail: a worker that never loads, or a stream that
        never produces video. Other formats keep their enhancer."""
        with self._lock:
            self.formats.discard(fmt)
            self._ready.discard(fmt)
            waiting = self._waiting.pop(fmt, None)
        if waiting:
            stop_process(waiting)


def stop_process(proc):
    """Stop a stream's video process. An enhancer worker runs helper programs, so its whole group goes."""
    try:
        if getattr(proc, "has_helpers", False):
            os.killpg(proc.pid, signal.SIGKILL)
        else:
            proc.kill()
    except ProcessLookupError:
        pass
    proc.wait()


# --------------------------------------------------------------------------- #
# Stream sessions
# --------------------------------------------------------------------------- #

class Sessions:
    def __init__(self, limit):
        self.limit = limit
        self._active = {}
        self._lock = threading.Lock()
        self._next = 1

    def acquire(self, channel, client):
        with self._lock:
            if len(self._active) >= self.limit:
                return None
            sid = self._next
            self._next += 1
            self._active[sid] = {"channel": channel, "client": client, "started": time.time()}
            return sid

    def set_input(self, sid, info):
        self.set(sid, "input", info)

    def set(self, sid, key, value):
        with self._lock:
            if sid in self._active:
                self._active[sid][key] = value

    def release(self, sid):
        with self._lock:
            self._active.pop(sid, None)

    def snapshot(self):
        with self._lock:
            now = time.time()
            return [{"id": k, "channel": v["channel"], "client": v["client"],
                     "seconds": int(now - v["started"]), "input": v.get("input"),
                     "processing": PIPELINE_NAMES.get(v.get("processing"), v.get("processing"))}
                    for k, v in self._active.items()]


def _pump_tuner_to_ffmpeg(upstream, proc, stop):
    try:
        while not stop.is_set():
            data = upstream.read1(CHUNK)   # pass along whatever has arrived, don't wait to fill CHUNK
            if not data:
                break
            proc.stdin.write(data)
    except (BrokenPipeError, ValueError, OSError):
        pass
    finally:
        stop.set()
        try:
            proc.stdin.close()
        except OSError:
            pass


# Normal when joining a broadcast mid-stream, before the first full picture arrives.
_HARMLESS = ("Invalid frame dimensions 0x0", "Last message repeated",
             "Error submitting packet to decoder")   # the first half second after joining a channel mid-picture

_LEVEL = re.compile(r"\[(trace|debug|verbose|info|warning|error|fatal|panic)\] ?")

_CODECS = {"mpeg2video": "MPEG-2", "h264": "H.264", "hevc": "HEVC", "ac3": "AC-3",
           "eac3": "E-AC-3", "aac": "AAC", "mp2": "MP2", "ac4": "AC-4"}


def describe_video(line):
    """'... Video: mpeg2video (Main) ..., yuv420p(tv, top first), 1920x1080 [SAR 1:1 DAR 16:9], 29.97 fps'
    -> {'label': '1080i', 'fps': '29.97', 'codec': 'MPEG-2', 'size': '1920x1080', 'aspect': '16:9'}"""
    codec = line.split("Video:", 1)[1].split()[0].strip(",")
    size = re.search(r"(\d{3,4})x(\d{3,4})", line)
    fps = re.search(r"([\d.]+) fps", line)
    dar = re.search(r"DAR (\d+:\d+)", line)
    interlaced = any(k in line for k in ("top first", "bottom first", "interlaced"))
    w, h = (int(size.group(1)), int(size.group(2))) if size else (0, 0)
    lines = min((2160, 1080, 720, 576, 480), key=lambda n: abs(n - h)) if h else 0
    return {
        "label": f"{lines}{'i' if interlaced else 'p'}" if lines else "unknown",
        "fps": fps.group(1) if fps else "?",
        "codec": _CODECS.get(codec, codec),
        "size": f"{w}x{h}" if size else "?",
        "aspect": dar.group(1) if dar else "?",
    }


def describe_audio(line):
    """'Stream #0:1[0x34](spa): Audio: ac3 (...), 48000 Hz, 5.1(side), fltp, 384 kb/s' -> 'AC-3 5.1 (spa)'"""
    codec = line.split("Audio:", 1)[1].split()[0].strip(",")
    parts = [x.strip() for x in line.split("Audio:", 1)[1].split(",")]
    layout = parts[2].split("(")[0] if len(parts) > 2 else ""
    lang = re.search(r"\]\((\w{3})\):", line)
    text = f"{_CODECS.get(codec, codec)} {layout}".strip()
    return f"{text} ({lang.group(1)})" if lang and lang.group(1) != "und" else text


def _read_ffmpeg_log(proc, channel, name, cfg, on_input, pipeline="cpu", remembered=None):
    in_input, video, audio, reported = False, None, [], False

    def report():
        nonlocal reported
        if reported or not video:
            return
        reported = True
        on_input({"video": video, "audio": audio})
        label, shown = video["label"], video
        if label == "unknown" and remembered:   # size not seen in the first second; the route was picked from memory
            label = remembered.get("label", "unknown")
            shown = dict(video, label=f"{label} (remembered)", aspect=remembered.get("aspect", "?"))
        out_codec = "HEVC" if "hevc" in cfg.encoder or pipeline == "ai" else "H.264"
        out_height = Enhancer.OUTPUT_HEIGHT.get(label, 1440) if pipeline == "ai" else cfg.height
        fps_out = {"60000/1001": "59.94", "30000/1001": "29.97"}.get(cfg.fps, cfg.fps)
        log.info("%s %s: %s %sfps %s %s, audio %s -> output %dp %sfps %s [%s]",
                 channel, name, shown["label"], shown["fps"], shown["codec"],
                 shown["aspect"], ", ".join(audio) or "none",
                 out_height, fps_out, out_codec, PIPELINE_NAMES.get(pipeline, pipeline))

    for raw in iter(proc.stderr.readline, b""):
        line = raw.decode(errors="replace").rstrip()
        # Lines look like "[info] msg" or "[libx264 @ 0x...] [info] msg".
        m = _LEVEL.search(line)
        level = m.group(1) if m else "error"
        msg = (line[:m.start()] + line[m.end():]).strip() if m else line.strip()
        if level in ("info", "verbose", "debug", "trace"):
            if msg.startswith("Input #0"):
                in_input = True
            elif msg.startswith(("Output #0", "Stream mapping")):
                in_input = False
                report()
            elif in_input and "Stream #0:" in msg and "->" not in msg:
                if "Video:" in msg and video is None:
                    video = describe_video(msg)
                elif "Audio:" in msg:
                    audio.append(describe_audio(msg))
            elif msg.startswith("enhancer:"):
                log.info("%s %s", channel, msg)      # the AI enhancer's speed over the whole stream
            continue   # other info lines are routine chatter
        if level == "warning":
            continue   # minor glitches from weak signal; only real errors are logged
        if msg and not any(h in msg for h in _HARMLESS):
            log.warning("ffmpeg [%s]: %s", channel, msg)
    report()


class Tune:
    """One channel being watched: the HDHomeRun connection, and the video process (ffmpeg, or the AI
    enhancer) that turns it into the stream Jellyfin gets."""

    def __init__(self, app, number, channel, sid, client):
        self.app, self.number, self.channel, self.sid, self.client = app, number, channel, sid, client
        self.name = channel.get("GuideName", "")
        self.upstream = self.proc = None
        self.enhanced = False               # is this stream using the AI enhancer's one slot?
        self.stop = threading.Event()

    def start_video(self):
        """Open the tuner and start a video process. If a processing method quits before its first
        video, it is turned off and the next simpler one is tried.
        Returns (first video bytes, None), or (None, (HTTP code, message)) when nothing worked."""
        pipeline = self._choose()
        while True:
            error = self._open(pipeline)
            if error:
                return None, error
            data = self.proc.stdout.read(CHUNK)
            if data:
                return data, None
            self.close()                                   # quit before producing any video
            if self.pipeline == "cpu":
                log.error("Video processing failed for %s (see ffmpeg errors above)", self.number)
                return None, (502, b"Video processing failed\n")
            pipeline = self._after_failure(self.pipeline)

    def read(self):
        """The next piece of the stream; empty once the tuner or the video process has stopped."""
        return b"" if self.stop.is_set() else self.proc.stdout.read(CHUNK)

    def close(self):
        """Stop the video process, give back the enhancer slot if this stream had it, free the tuner."""
        self.stop.set()
        if self.proc:
            stop_process(self.proc)
            self.proc = None
        if self.enhanced:
            self.app.enhancer.release()
            self.enhanced = False
        if self.upstream:
            self.upstream.close()           # frees the HDHomeRun tuner
            self.upstream = None

    def _choose(self):
        app = self.app
        return choose_pipeline(app.cfg, app.formats.get(self.number), app.failed_pipelines, app.enhancer.formats)

    def _open(self, pipeline):
        """Open the tuner ourselves (so "all tuners busy" becomes a clean 503), then start the
        video process. Returns None, or (HTTP code, message) if the tuner could not be opened."""
        app, number = self.app, self.number
        try:
            self.upstream = urllib.request.urlopen(self.channel["URL"], timeout=TUNER_TIMEOUT)
        except urllib.error.HTTPError as e:
            log.info("HDHomeRun refused %s: HTTP %s", number, e.code)
            return (503 if e.code == 503 else 502, b"Tuner unavailable\n")
        except Exception as e:
            log.error("HDHomeRun error on %s: %s", number, e)
            return (502, b"Cannot reach HDHomeRun\n")

        def on_input(info):
            app.sessions.set_input(self.sid, info)
            app.formats.update(number, info["video"])

        self.stop = threading.Event()
        remembered = app.formats.get(number)
        if pipeline == "ai":
            self.proc = app.enhancer.take(remembered["label"])
            self.enhanced = self.proc is not None
            if not self.enhanced:    # still loading, or another stream has it: standard path this time
                pipeline = choose_pipeline(app.cfg, remembered, app.failed_pipelines)
                log.info("AI enhancer is busy or still loading; %s gets %s this time",
                         number, PIPELINE_NAMES[pipeline])
        if not self.enhanced:
            self.proc = subprocess.Popen(build_ffmpeg_cmd(app.cfg, pipeline),
                                         stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                         stderr=subprocess.PIPE, bufsize=0)
        self.pipeline = pipeline
        threading.Thread(target=_pump_tuner_to_ffmpeg, args=(self.upstream, self.proc, self.stop),
                         daemon=True).start()
        threading.Thread(target=_read_ffmpeg_log,
                         args=(self.proc, number, self.name, app.cfg, on_input, pipeline, remembered),
                         daemon=True).start()
        app.sessions.set(self.sid, "processing", pipeline)
        log.info("Streaming %s (%s) to %s using %s", number, self.name, self.client, PIPELINE_NAMES[pipeline])
        return None

    def _after_failure(self, pipeline):
        """A processing method quit before its first video: turn it off until the upscaler restarts
        and return the next one to try."""
        if pipeline == "ai":
            fmt = self.app.formats.get(self.number)["label"]
            self.app.enhancer.give_up(fmt)
            still_off = f"The AI enhancer stays off for {fmt} channels until the upscaler restarts."
        else:
            self.app.failed_pipelines.add(pipeline)
            still_off = "It stays off until the upscaler restarts."
        failed_name, pipeline = PIPELINE_NAMES[pipeline], self._choose()
        log.warning("%s didn't work for %s; switching to %s. %s", failed_name[0].upper() + failed_name[1:],
                    self.number, PIPELINE_NAMES[pipeline], still_off)
        return pipeline


# --------------------------------------------------------------------------- #
# HTTP server
# --------------------------------------------------------------------------- #

class Handler(BaseHTTPRequestHandler):
    server_version = "ota-upscaler/1.0"
    protocol_version = "HTTP/1.0"  # stream until we close; no chunked encoding

    def log_message(self, fmt, *args):
        log.debug("%s %s", self.address_string(), fmt % args)

    @property
    def app(self):
        return self.server.app

    def do_HEAD(self):
        self._route(head=True)

    def do_GET(self):
        self._route(head=False)

    def _route(self, head):
        path = urlparse(self.path).path
        if path in ("/", "/playlist.m3u", "/playlist.m3u8"):
            self._playlist(head)
        elif path.startswith("/stream/"):
            self._stream(unquote(path[len("/stream/"):]), head)
        elif path in ("/guide.xml", "/xmltv.xml"):
            self._guide(head)
        elif path == "/status":
            status = {"streams": self.app.sessions.snapshot()}
            if self.app.guide:
                _, updated, error = self.app.guide.snapshot()
                status["guide"] = {
                    "updated": time.strftime("%Y-%m-%d %H:%M", time.localtime(updated)) if updated else None,
                    "error": error,
                }
            self._send(200, "application/json", json.dumps(status, indent=2).encode(), head)
        else:
            self._send(404, "text/plain", b"Not found\n", head)

    def _send(self, code, ctype, body, head=False):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if not head:
            self.wfile.write(body)

    # ---- playlist -------------------------------------------------------- #

    def _playlist(self, head):
        try:
            channels = self.app.lineup.channels()
        except Exception as e:
            log.error("Cannot reach HDHomeRun: %s", e)
            return self._send(502, "text/plain", b"Cannot reach HDHomeRun\n", head)

        host = self.app.cfg.public_host or self.headers.get("Host") \
            or f"{self.server.server_address[0]}:{self.app.cfg.port}"
        if ":" not in host:
            host = f"{host}:{self.app.cfg.port}"

        logos = self.app.guide.logos if self.app.guide else {}
        lines = [f'#EXTM3U url-tvg="http://{host}/guide.xml"' if self.app.guide else "#EXTM3U"]
        for ch in channels:
            num, name = str(ch["GuideNumber"]), ch.get("GuideName", "")
            logo = f' tvg-logo="{logos[num]}"' if num in logos else ""
            lines.append(
                f'#EXTINF:-1 tvg-id="{num}" tvg-chno="{num}" tvg-name="{name}"{logo} '
                f'group-title="OTA",{num} {name}'
            )
            lines.append(f"http://{host}/stream/{quote(num)}")
        self._send(200, "audio/x-mpegurl", ("\n".join(lines) + "\n").encode(), head)

    # ---- guide ----------------------------------------------------------- #

    def _guide(self, head):
        guide = self.app.guide
        if guide is None:
            return self._send(404, "text/plain", b"Guide disabled (--no-guide)\n", head)
        xml, _, error = guide.snapshot()
        if xml is None:
            msg = (error or "Guide not downloaded yet, try again in a minute") + "\n"
            return self._send(503, "text/plain", msg.encode(), head)
        self._send(200, "application/xml; charset=utf-8", xml, head)

    # ---- stream ---------------------------------------------------------- #

    def _stream(self, number, head):
        ch = self.app.lineup.get(number) if number else None
        if not ch:
            return self._send(404, "text/plain", b"Unknown channel\n", head)
        if head:
            self.send_response(200)
            self.send_header("Content-Type", "video/mp2t")
            self.end_headers()
            return

        client = self.address_string()
        sid = self.app.sessions.acquire(number, client)
        if sid is None:
            log.info("Rejected %s for %s: stream limit reached", number, client)
            return self._send(503, "text/plain", b"All tuners busy\n")

        tune = Tune(self.app, number, ch, sid, client)
        try:
            started = time.monotonic()
            data, error = tune.start_video()
            if error:
                code, message = error
                return self._send(code, "text/plain", message)
            log.info("First video for %s after %.1fs", number, time.monotonic() - started)
            self.send_response(200)
            self.send_header("Content-Type", "video/mp2t")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            while data:
                self.wfile.write(data)
                data = tune.read()
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            pass  # client changed channel or stopped watching
        finally:
            tune.close()
            self.app.sessions.release(sid)
            log.info("Stopped %s for %s", number, client)


class App:
    def __init__(self, cfg):
        self.cfg = cfg
        self.lineup = Lineup(cfg.hdhr)
        self.sessions = Sessions(cfg.max_streams)
        self.guide = None if cfg.no_guide else Guide(cfg.hdhr, cfg.guide_api, self.lineup)
        self.formats = Formats(pathlib.Path(__file__).with_name("channel_formats.json"))
        self.enhancer = Enhancer(cfg)
        self.failed_pipelines = set()


def main(argv=None):
    cfg = parse_args(argv)
    logging.basicConfig(level=cfg.log_level.upper(),
                        format="%(asctime)s %(levelname)s %(message)s")
    if not shutil.which(cfg.ffmpeg):
        sys.exit(f"ffmpeg not found ({cfg.ffmpeg}). Install it with: brew install ffmpeg")

    app = App(cfg)
    try:
        app.lineup.channels()
    except Exception as e:
        log.warning("HDHomeRun not reachable yet (%s); will retry on first request", e)

    if app.guide:
        app.guide.start()
    app.enhancer.start()

    server = ThreadingHTTPServer((cfg.bind, cfg.port), Handler)
    server.daemon_threads = True
    server.app = app
    signal.signal(signal.SIGTERM, lambda *_: threading.Thread(target=server.shutdown).start())
    log.info("Listening on http://%s:%d/playlist.m3u (encoder %s, processing %s, max %d streams, "
             "AI enhancer %s)", cfg.bind, cfg.port, cfg.encoder, cfg.processing, cfg.max_streams,
             "on for %s channels" % " and ".join(sorted(app.enhancer.formats)) if app.enhancer.formats else "off")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        app.enhancer.stop()


if __name__ == "__main__":
    main()
