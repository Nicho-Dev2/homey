#!/usr/bin/env python3
"""
Benchmark Apple's real-time AI upscaler (VideoToolbox low-latency super resolution)
on a clip recorded from the HDHomeRun.

    python3 ai_upscale_bench.py info
        What this Mac supports: scale factors, size limits, pixel formats.

    python3 ai_upscale_bench.py run clip_720p.ts --seconds 30
        Runs the AI modes on the clip, reports their speed, and writes files to
        compare on the TV:
          clip_720p_original_720p.ts   the broadcast as-is (what you watch today)
          clip_720p_plain_4k.mp4       normal scaling to 4K, no AI
          clip_720p_ai_4k.mp4          AI 1.5x (720p -> 1080p), then stretched to 4K
          clip_720p_ai2x_4k.mp4        AI 2x in two overlapping tiles (720p -> 1440p), then to 4K
          clip_720p_ai2x_sharp_4k.mp4  same as ai2x, plus light sharpening
          clip_720p_aiq_4k.mp4         Apple's high-quality model, which also looks at the
                                       previous frame (720p -> 1440p), then to 4K

    python3 ai_upscale_bench.py run clip_720p.ts --no-output --snapshot 60
        Saves clip_720p_compare.png: the frame at 60 seconds from every version,
        labelled, in a grid. Top row: the broadcast as it arrives. Bottom row: the
        same, after cleaning up the broadcast's compression damage first.

Modes (--modes, default ai2x,tnr2x,aiq,aiq540):
    ai     real-time model at 1.5x, whole frame
    ai2x   real-time model at 2x, in two tiles
    tnr2x  Apple's AI noise filter (uses the previous frames), then ai2x
    aiq540 high-quality model fed a 960x540 picture, so its 4x lands exactly on 4K
    aiq    high-quality model at 2x, or 4x if that's all the Mac offers (first use
           downloads it from Apple, once); the result is then brought to 4K

Options for run:
    --seconds N       only process the first N seconds (default: whole clip)
    --no-output       measure speed only, don't write files
    --modes ai2x,aiq  which AI modes to run
    --denoise 0.5     strength of the AI noise filter for tnr2x, 0 to 1
    --parallel-tiles  run the two 2x tiles at the same time instead of one after the other
    --pipeline        tnr2x: denoise the next frame while upscaling the current one
    --sharpness 0.4   strength of the light sharpening, 0 to 1 (default 0.4)
    --snapshot 60     make the comparison picture at 60 seconds. With --no-output
                      the run jumps to just before that point and stops right
                      after it, so it only takes a minute.
    --crop X,Y        top-left corner (in 4K pixels) of the area shown in the
                      snapshot, default the middle of the picture
    --no-cleanup      leave the cleanup row out of the snapshot
    --cleanup-filter  the ffmpeg filters used for cleanup, default
                      "spp=quality=2,hqdn3d=1.5:1:2:2,deband" (deblock, light
                      denoise, smooth out banding)

Needs:  brew install python ffmpeg
        pip3 install pyobjc numpy   (inside the bench .venv)
"""

import argparse
import json
import os
import platform
import shutil
import subprocess
import sys
import threading
import time
from pathlib import Path

REALTIME_FPS = 60000 / 1001   # 59.94, what live TV needs after deinterlacing
FRAME_BUDGET_MS = 1000 / REALTIME_FPS
TARGET_W, TARGET_H = 3840, 2160
TILE_WIDTHS = (704, 672, 736, 768, 800, 832, 864, 896, 928, 960)   # tried in order for 2x tiles
DEFAULT_CLEANUP = "spp=quality=2,hqdn3d=1.5:1:2:2,deband"
WARMUP_FRAMES = 120   # frames run before the snapshot frame, so models that use earlier frames settle

# Raw values of Apple's enums for the high-quality model. Apple doesn't publish the
# numbers for Python; these follow the pattern of its other frame processors and can
# be overridden with the hidden --q-enums option if Apple's numbering turns out different.
Q_ENUMS = {"input": 1,        # InputType: video = 1, image = 2
           "quality": 1,      # QualityPrioritization: normal = 1
           "random": 1,       # SubmissionMode: random = 1 (first frame, clears history)
           "sequential": 2,   # SubmissionMode: sequential = 2 (follows the previous frame)
           "ready": 2}        # ModelStatus: download required = 0, downloading = 1, ready = 2

# CoreVideo pixel formats -> ffmpeg raw formats, with each plane's (bytes per row, rows)
PIXEL_FORMATS = {
    "420v": ("nv12", lambda w, h: [(w, h), (w, h // 2)]),
    "420f": ("nv12", lambda w, h: [(w, h), (w, h // 2)]),
    # 16-bit half-float RGBA, one plane of 8 bytes per pixel. Apple's high-quality
    # model only takes this; the benchmark converts to and from it.
    "RGhA": ("rgbaf16le", lambda w, h: [(w * 8, h)]),
    # 10-bit 4:2:0, two planes, 2 bytes per sample (same layout as ffmpeg's p010).
    "x420": ("p010le", lambda w, h: [(w * 2, h), (w * 2, h // 2)]),
    # 8-bit RGB with alpha, one plane of 4 bytes per pixel.
    "BGRA": ("bgra", lambda w, h: [(w * 4, h)]),
}
# Formats the noise filter can be fed, best first (the benchmark converts to and from them).
NOISE_FORMATS = ("420v", "x420", "RGhA", "BGRA")
# Apple's compressed in-memory formats. The CPU can't write them directly, so the
# benchmark fills a normal buffer and lets VideoToolbox's pixel transfer (on the
# GPU) copy it across. Lossless first; each is the compressed twin of 420v.
COMPRESSED_420V = ("&8v0", "-8v0", "/8v0", "|8v0")


class PixelTransfer:
    """VTPixelTransferSession: copies/converts one pixel buffer into another."""

    def __init__(self):
        try:
            import VideoToolbox as VT
        except ImportError:
            raise RuntimeError("needs PyObjC's VideoToolbox bindings: pip install pyobjc-framework-VideoToolbox")
        self.VT = VT
        res = VT.VTPixelTransferSessionCreate(None, None)
        status, sess = res if isinstance(res, tuple) else (0, res)
        if status != 0 or sess is None:
            raise RuntimeError(f"couldn't start a pixel transfer session ({status})")
        self.sess = sess

    def copy(self, src, dst):
        status = self.VT.VTPixelTransferSessionTransferImage(self.sess, src, dst)
        if isinstance(status, tuple):
            status = status[0]
        if status != 0:
            raise RuntimeError(f"pixel transfer failed ({status})")
YUV_TAGS = "in_color_matrix=bt709:in_range=tv"   # broadcast HD is BT.709, limited range


def nv12_to_rgbah(y, uv, out):
    """Convert one NV12 frame (BT.709, limited range) to half-float RGBA in `out`
    (shape h x w x 4, float16), values 0-1 like Apple's RGhA buffers expect."""
    import numpy as np
    h, w = y.shape
    Y = (y.astype(np.float32) - 16) * (1 / 219)
    c = (uv.reshape(h // 2, w // 2, 2).astype(np.float32) - 128) * (1 / 224)
    c = c.repeat(2, axis=0).repeat(2, axis=1)
    U, V = c[..., 0], c[..., 1]
    out[..., 0] = np.clip(Y + 1.5748 * V, 0, 1)
    out[..., 1] = np.clip(Y - 0.1873 * U - 0.4681 * V, 0, 1)
    out[..., 2] = np.clip(Y + 1.8556 * U, 0, 1)
    out[..., 3] = 1


def rgb_to_nv12(rgb, y, uv):
    """Float RGB (h x w x 3+, 0-1) back to NV12 planes (BT.709, limited range)."""
    import numpy as np
    r, g, b = (rgb[..., i].astype(np.float32) for i in range(3))
    yn = 0.2126 * r + 0.7152 * g + 0.0722 * b
    y[:] = np.clip(16 + 219 * yn + 0.5, 0, 255).astype(np.uint8)
    cb = (b - yn) / 1.8556
    cr = (r - yn) / 1.5748
    h, w = yn.shape
    pool = lambda c: c.reshape(h // 2, 2, w // 2, 2).mean(axis=(1, 3))
    out = np.empty((h // 2, w // 2, 2), np.float32)
    out[..., 0], out[..., 1] = pool(cb), pool(cr)
    uv[:] = np.clip(128 + 224 * out + 0.5, 0, 255).astype(np.uint8).reshape(h // 2, w)


class FormatBridge:
    """Converts NV12 planes to another pixel format and back, on the CPU."""

    def __init__(self, fmt, w, h):
        import numpy as np
        self.np, self.fmt, self.w, self.h = np, fmt, w, h
        if fmt in ("RGhA", "BGRA"):
            self.rgb = np.zeros((h, w, 4), np.float16 if fmt == "RGhA" else np.float32)

    def to(self, planes):
        np, fmt = self.np, self.fmt
        if fmt in ("420v", "420f"):
            return planes
        if fmt == "x420":   # 10-bit, value in the top bits of each 16-bit sample
            return [(p.astype(np.uint16) << 8).view(np.uint8) for p in planes]
        nv12_to_rgbah(planes[0], planes[1], self.rgb)
        if fmt == "RGhA":
            return [self.rgb.view(np.uint8).reshape(self.h, -1)]
        bgra = np.empty((self.h, self.w, 4), np.uint8)
        bgra[..., 0] = np.clip(self.rgb[..., 2] * 255 + 0.5, 0, 255)
        bgra[..., 1] = np.clip(self.rgb[..., 1] * 255 + 0.5, 0, 255)
        bgra[..., 2] = np.clip(self.rgb[..., 0] * 255 + 0.5, 0, 255)
        bgra[..., 3] = 255
        return [bgra.reshape(self.h, -1)]

    def back(self, planes, into):
        np, fmt = self.np, self.fmt
        if fmt in ("420v", "420f"):
            for d, s in zip(into, planes):
                d[:] = s
            return into
        if fmt == "x420":
            for d, s in zip(into, planes):
                v = s.view(np.uint16).astype(np.uint32)
                d[:] = np.minimum((v + 128) >> 8, 255).astype(np.uint8)
            return into
        if fmt == "RGhA":
            rgb = planes[0].view(np.float16).reshape(self.h, self.w, 4)
        else:
            px = planes[0].reshape(self.h, self.w, 4).astype(np.float32) / 255
            rgb = px[..., [2, 1, 0]]
        rgb_to_nv12(rgb, into[0], into[1])
        return into


def resample_matrix(n_in, n_out):
    """Lanczos-3 resampling as a matrix (n_out x n_in), for shrinking with numpy."""
    import numpy as np
    scale = max(n_in / n_out, 1.0)
    m = np.zeros((n_out, n_in), np.float32)
    for i in range(n_out):
        centre = (i + 0.5) * n_in / n_out - 0.5
        lo, hi = int(np.floor(centre - 3 * scale)), int(np.ceil(centre + 3 * scale))
        xs = np.arange(lo, hi + 1)
        t = (xs - centre) / scale
        wts = np.sinc(t) * np.sinc(t / 3) * (np.abs(t) < 3)
        for x, wt in zip(np.clip(xs, 0, n_in - 1), wts):
            m[i, x] += wt
        m[i] /= m[i].sum()
    return m


def shrink(img, ry, rx):
    """Resize an h x w x c float image with the matrices from resample_matrix."""
    import numpy as np
    h, w, c = img.shape
    tmp = (ry @ img.reshape(h, w * c)).reshape(ry.shape[0], w, c)
    return np.tensordot(tmp, rx, axes=([1], [1])).transpose(0, 2, 1)


def fourcc(code):
    return int(code).to_bytes(4, "big").decode("ascii", "replace")


def fourcc_int(text):
    return int.from_bytes(text.encode("ascii"), "big")


# --------------------------------------------------------------------------- #
# Hide Apple's model-setup notes ("Resolved compile flags: ...")
# --------------------------------------------------------------------------- #

def quiet_apple_logs():
    """Route this process's stderr through a filter that drops Apple's verbose
    model-compilation notes but passes everything else (real errors) through."""
    real = os.dup(2)
    r, w = os.pipe()
    os.dup2(w, 2)
    os.close(w)

    def pump():
        skipping = False
        with os.fdopen(r, "rb") as src, os.fdopen(real, "wb", buffering=0) as dst:
            for line in src:
                text = line.decode(errors="replace").strip()
                if "Resolved compile flags" in text:
                    skipping = True
                    continue
                if skipping:
                    if text.startswith(")"):
                        skipping = False
                    continue
                dst.write(line)

    threading.Thread(target=pump, daemon=True).start()


# --------------------------------------------------------------------------- #
# Apple VideoToolbox, through PyObjC
# --------------------------------------------------------------------------- #

class AppleSR:
    """Access to VTFrameProcessor + VTLowLatencySuperResolutionScaler."""

    def __init__(self):
        try:
            import objc
            import Quartz  # CoreVideo functions live here in PyObjC
        except ImportError:
            sys.exit("PyObjC isn't installed. Run:  pip3 install pyobjc numpy")
        self.objc, self.Q = objc, Quartz
        objc.loadBundle("VideoToolbox", bundle_path="/System/Library/Frameworks/VideoToolbox.framework",
                        module_globals={})
        try:
            self.Config = objc.lookUpClass("VTLowLatencySuperResolutionScalerConfiguration")
            self.Params = objc.lookUpClass("VTLowLatencySuperResolutionScalerParameters")
            self.Processor = objc.lookUpClass("VTFrameProcessor")
            self.Frame = objc.lookUpClass("VTFrameProcessorFrame")
        except objc.nosuchclass_error as e:
            sys.exit(f"This macOS doesn't have the real-time super resolution API ({e}).")
        # PyObjC has no built-in description of these newer methods, so tell it the
        # last argument is an "NSError **" it should fill in and hand back.
        for sel in (b"startSessionWithConfiguration:error:", b"processWithParameters:error:"):
            try:
                objc.registerMetaDataForSelector(
                    b"VTFrameProcessor", sel,
                    {"retval": {"type": objc._C_NSBOOL},
                     "arguments": {3: {"type_modifier": objc._C_OUT, "null_accepted": True}}})
            except Exception:
                pass
        # Apple's AI temporal noise filter, also optional.
        self.TConfig = self.TParams = None
        self.t_missing = None
        try:
            self.TConfig = objc.lookUpClass("VTTemporalNoiseFilterConfiguration")
            self.TParams = objc.lookUpClass("VTTemporalNoiseFilterParameters")
        except objc.nosuchclass_error as e:
            self.t_missing = str(e)
        # The high-quality model (VTSuperResolutionScaler) is optional: older macOS lacks it.
        self.QConfig = self.QParams = None
        self.q_missing = None
        try:
            self.QConfig = objc.lookUpClass("VTSuperResolutionScalerConfiguration")
            self.QParams = objc.lookUpClass("VTSuperResolutionScalerParameters")
        except objc.nosuchclass_error as e:
            self.q_missing = str(e)
        if self.QConfig is not None:
            # The model download reports back through a block taking one NSError.
            try:
                objc.registerMetaDataForSelector(
                    b"VTSuperResolutionScalerConfiguration",
                    b"downloadConfigurationModelWithCompletionHandler:",
                    {"arguments": {2: {"callable": {"retval": {"type": b"v"},
                                                    "arguments": {0: {"type": b"^v"},
                                                                  1: {"type": b"@"}}}}}})
            except Exception:
                pass

    @staticmethod
    def ok(result):
        """Calls return (ok, error) when PyObjC knows about the error argument, else just ok."""
        if isinstance(result, tuple):
            return bool(result[0]), result[1]
        return bool(result), None

    # ---- capability report ------------------------------------------------ #

    def supported(self):
        return bool(self.Config.isSupported())

    def scale_factors(self, w, h):
        try:
            return [float(x) for x in self.Config.supportedScaleFactorsForFrameWidth_frameHeight_(w, h)]
        except Exception as e:
            return f"error: {e}"

    def limits(self):
        out = {}
        for name in ("maximumDimensions", "minimumDimensions"):
            try:
                d = getattr(self.Config, name)()
                out[name] = f"{d[0]}x{d[1]}" if isinstance(d, tuple) else str(d)
            except Exception as e:
                out[name] = f"error: {e}"
        try:
            out["supportedScaleFactors (any size)"] = [float(x) for x in self.Config.supportedScaleFactors()]
        except Exception as e:
            out["supportedScaleFactors (any size)"] = f"error: {e}"
        for f in (2.0, 3.0, 4.0):
            for name in ("maximumDimensionForSpatialScaleFactor_", "maximumPixelCountForSpatialScaleFactor_"):
                try:
                    v = getattr(self.Config, name)(f)
                    out[f"{name.rstrip('_')}({f:g})"] = None if v is None else int(v)
                except Exception as e:
                    out[f"{name.rstrip('_')}({f:g})"] = f"error: {e}"
        return out

    def config(self, w, h, factor):
        cfg = self.Config.alloc().initWithFrameWidth_frameHeight_scaleFactor_(w, h, factor)
        if cfg is None:
            raise RuntimeError(f"Apple refused {w}x{h} at {factor:g}x")
        return cfg

    def pixel_formats(self, cfg):
        for name in ("frameSupportedPixelFormats", "supportedPixelFormats"):
            try:
                return [fourcc(x) for x in getattr(cfg, name)()]
            except Exception:
                continue
        return []

    def session(self, w, h, factor, fmt):
        return AppleSession(self, w, h, factor, fmt)

    # ---- high-quality model ----------------------------------------------- #

    def q_supported(self):
        if self.QConfig is None:
            return False
        try:
            return bool(self.QConfig.isSupported())
        except Exception:
            return False

    def q_scale_factors(self):
        try:
            return [int(x) for x in self.QConfig.supportedScaleFactors()]
        except Exception as e:
            return f"error: {e}"

    def q_config(self, w, h, factor):
        C = self.QConfig
        try:
            revision = int(C.defaultRevision())
        except Exception:
            revision = 1
        cfg = C.alloc().initWithFrameWidth_frameHeight_scaleFactor_inputType_usePrecomputedFlow_qualityPrioritization_revision_(
            w, h, int(factor), Q_ENUMS["input"], False, Q_ENUMS["quality"], revision)
        if cfg is None:
            raise RuntimeError(f"Apple refused {w}x{h} at {factor:g}x for the high-quality model")
        return cfg

    def q_pixel_formats(self, w, h, factor):
        return self.pixel_formats(self.q_config(w, h, factor))

    def q_status(self, cfg):
        try:
            return int(cfg.configurationModelStatus())
        except Exception:
            return None

    def q_ensure_model(self, cfg, log=print):
        """The high-quality model isn't built into macOS; Apple downloads it on first use."""
        status = self.q_status(cfg)
        if status is None or status == Q_ENUMS["ready"]:
            return
        log("  aiq   downloading Apple's high-quality model (one time only)...")
        done, box = threading.Event(), {}

        def handler(err):
            box["err"] = err
            done.set()
        cfg.downloadConfigurationModelWithCompletionHandler_(handler)
        last = -1
        start = time.monotonic()
        while not done.wait(2):
            try:
                v = float(cfg.configurationModelPercentageAvailable())
                pct = int(v if v > 1 else v * 100)   # 0-1 or 0-100, depending on Apple
            except Exception:
                pct = None
            if pct is not None and pct != last and pct % 10 == 0:
                log(f"        {pct}%")
                last = pct
            if time.monotonic() - start > 1800:
                raise RuntimeError("model download didn't finish within 30 minutes")
        if box.get("err"):
            raise RuntimeError(f"model download failed: {box['err']}")
        status = self.q_status(cfg)
        if status is not None and status != Q_ENUMS["ready"]:
            raise RuntimeError(f"model still not ready after downloading (status {status})")
        log("        done")

    def q_session(self, w, h, factor, fmt):
        return AppleQualitySession(self, w, h, factor, fmt)

    # ---- temporal noise filter -------------------------------------------- #

    def t_supported(self):
        if self.TConfig is None:
            return False
        try:
            return bool(self.TConfig.isSupported())
        except Exception:
            return False

    def t_config(self, w, h, fmt):
        cfg = self.TConfig.alloc().initWithFrameWidth_frameHeight_sourcePixelFormat_(w, h, fourcc_int(fmt))
        if cfg is None:
            raise RuntimeError(f"Apple refused {w}x{h} in {fmt} for the noise filter")
        return cfg

    def t_formats(self):
        try:
            return [fourcc(x) for x in self.TConfig.supportedSourcePixelFormats()]
        except Exception:
            return []

    def t_info(self, w, h, fmt):
        out = {}
        try:
            out["source formats"] = [fourcc(x) for x in self.TConfig.supportedSourcePixelFormats()]
        except Exception as e:
            out["source formats"] = f"error: {e}"
        try:
            cfg = self.t_config(w, h, fmt)
            out[f"{w}x{h} {fmt}"] = (f"OK, previous frames up to {int(cfg.previousFrameCount())}, "
                                     f"next frames up to {int(cfg.nextFrameCount())}, "
                                     f"pixel formats {self.pixel_formats(cfg)}")
        except Exception as e:
            out[f"{w}x{h} {fmt}"] = f"not available ({e})"
        return out

    def t_session(self, w, h, fmt, strength, via=None):
        return AppleNoiseSession(self, w, h, fmt, strength, via)


class AppleSession:
    """One running model for one input size: a source buffer in, a destination buffer out."""

    def __init__(self, sr, w, h, factor, fmt):
        self.sr, self.Q = sr, sr.Q
        self.w, self.h = w, h
        self.cfg = sr.config(w, h, factor)
        self.proc = sr.Processor.alloc().init()
        t = time.monotonic()
        ok, err = sr.ok(self.proc.startSessionWithConfiguration_error_(self.cfg, None))
        if not ok:
            raise RuntimeError(f"Apple's model wouldn't start for {w}x{h} at {factor:g}x: "
                               f"{err or 'no details given'}")
        self.load_seconds = time.monotonic() - t
        self.out_w, self.out_h = int(round(w * factor)), int(round(h * factor))
        code = fourcc_int(fmt)
        _, self.layout = PIXEL_FORMATS[fmt]
        self.src = self._buffer(w, h, code, self.cfg.sourcePixelBufferAttributes())
        self.dst = self._buffer(self.out_w, self.out_h, code, self.cfg.destinationPixelBufferAttributes())
        self._sync = self.proc.respondsToSelector_(b"processWithParameters:error:")

    def _buffer(self, w, h, code, attrs):
        Q = self.Q
        attrs = dict(attrs or {})
        attrs.setdefault(Q.kCVPixelBufferIOSurfacePropertiesKey, {})
        attrs[Q.kCVPixelBufferPixelFormatTypeKey] = code
        attrs[Q.kCVPixelBufferWidthKey] = w
        attrs[Q.kCVPixelBufferHeightKey] = h
        ret, buf = Q.CVPixelBufferCreate(None, w, h, code, attrs, None)
        if ret != 0 or buf is None:
            raise RuntimeError(f"CVPixelBufferCreate {w}x{h} failed ({ret})")
        return buf

    def _planes(self, buf, w, h):
        import numpy as np
        Q, views = self.Q, []
        planar = bool(Q.CVPixelBufferIsPlanar(buf))
        for i, (row_bytes, rows) in enumerate(self.layout(w, h)):
            if planar:
                base = Q.CVPixelBufferGetBaseAddressOfPlane(buf, i)
                stride = Q.CVPixelBufferGetBytesPerRowOfPlane(buf, i)
            else:   # single-plane formats such as RGhA
                base = Q.CVPixelBufferGetBaseAddress(buf)
                stride = Q.CVPixelBufferGetBytesPerRow(buf)
            mem = base.as_buffer(stride * rows)
            views.append(np.ndarray((rows, stride), np.uint8, buffer=mem)[:, :row_bytes])
        return views

    def put(self, planes):
        """Copy numpy planes (Y, UV) into the source buffer."""
        self.Q.CVPixelBufferLockBaseAddress(self.src, 0)
        try:
            for view, plane in zip(self._planes(self.src, self.w, self.h), planes):
                view[:] = plane
        finally:
            self.Q.CVPixelBufferUnlockBaseAddress(self.src, 0)

    def run(self, index):
        pts = (index * 1001, 60000, 1, 0)   # CMTime(value, timescale, flags=valid, epoch)
        s = self.sr.Frame.alloc().initWithBuffer_presentationTimeStamp_(self.src, pts)
        d = self.sr.Frame.alloc().initWithBuffer_presentationTimeStamp_(self.dst, pts)
        params = self.sr.Params.alloc().initWithSourceFrame_destinationFrame_(s, d)
        if self._sync:
            ok, err = self.sr.ok(self.proc.processWithParameters_error_(params, None))
            if not ok:
                raise RuntimeError(f"Apple's model failed on frame {index}: {err or 'no details given'}")
        else:
            done, box = threading.Event(), {}

            def handler(_params, err):
                box["err"] = err
                done.set()
            self.proc.processWithParameters_completionHandler_(params, handler)
            done.wait()
            if box.get("err"):
                raise RuntimeError(f"Apple's model failed on frame {index}: {box['err']}")

    def get(self, into):
        """Copy the destination buffer into numpy planes `into` (Y, UV)."""
        self.Q.CVPixelBufferLockBaseAddress(self.dst, 1)
        try:
            for view, plane in zip(self._planes(self.dst, self.out_w, self.out_h), into):
                plane[:] = view
        finally:
            self.Q.CVPixelBufferUnlockBaseAddress(self.dst, 1)

    def stop(self):
        try:
            self.proc.endSession()
        except Exception:
            pass


class AppleQualitySession(AppleSession):
    """
    The high-quality model. It looks at the previous source frame and its own
    previous output as well as the current frame, so it keeps two source and two
    destination buffers and alternates between them: last frame's pair stays
    untouched while the new frame is processed.
    """

    def __init__(self, sr, w, h, factor, fmt):
        self.sr, self.Q = sr, sr.Q
        self.w, self.h = w, h
        self.cfg = sr.q_config(w, h, factor)
        t = time.monotonic()
        sr.q_ensure_model(self.cfg)
        self.proc = sr.Processor.alloc().init()
        ok, err = sr.ok(self.proc.startSessionWithConfiguration_error_(self.cfg, None))
        if not ok:
            raise RuntimeError(f"Apple's high-quality model wouldn't start for {w}x{h} at {factor:g}x: "
                               f"{err or 'no details given'}")
        self.load_seconds = time.monotonic() - t
        self.out_w, self.out_h = w * int(factor), h * int(factor)
        code = fourcc_int(fmt)
        _, self.layout = PIXEL_FORMATS[fmt]
        src_attrs = self.cfg.sourcePixelBufferAttributes()
        dst_attrs = self.cfg.destinationPixelBufferAttributes()
        self.srcs = [self._buffer(w, h, code, src_attrs) for _ in range(2)]
        self.dsts = [self._buffer(self.out_w, self.out_h, code, dst_attrs) for _ in range(2)]
        self._sync = self.proc.respondsToSelector_(b"processWithParameters:error:")
        self.k = 0   # frames processed so far in this session

    def put(self, planes):
        self.src = self.srcs[self.k % 2]
        super().put(planes)

    def run(self, index):
        F, k = self.sr.Frame, self.k
        pts = (index * 1001, 60000, 1, 0)
        prev_pts = ((index - 1) * 1001, 60000, 1, 0)
        cur_src, cur_dst = self.srcs[k % 2], self.dsts[k % 2]
        s = F.alloc().initWithBuffer_presentationTimeStamp_(cur_src, pts)
        d = F.alloc().initWithBuffer_presentationTimeStamp_(cur_dst, pts)
        if k == 0:
            ps = pd = None
            mode = Q_ENUMS["random"]
        else:
            ps = F.alloc().initWithBuffer_presentationTimeStamp_(self.srcs[(k - 1) % 2], prev_pts)
            pd = F.alloc().initWithBuffer_presentationTimeStamp_(self.dsts[(k - 1) % 2], prev_pts)
            mode = Q_ENUMS["sequential"]
        params = self.sr.QParams.alloc().initWithSourceFrame_previousFrame_previousOutputFrame_opticalFlow_submissionMode_destinationFrame_(
            s, ps, pd, None, mode, d)
        if params is None:
            raise RuntimeError("Apple refused the high-quality model's frame settings")
        self._process(params, index)
        self.dst = cur_dst
        self.k += 1

    def _process(self, params, index):
        if self._sync:
            ok, err = self.sr.ok(self.proc.processWithParameters_error_(params, None))
            if not ok:
                raise RuntimeError(f"Apple's high-quality model failed on frame {index}: "
                                   f"{err or 'no details given'}")
            return
        done, box = threading.Event(), {}

        def handler(_params, err):
            box["err"] = err
            done.set()
        self.proc.processWithParameters_completionHandler_(params, handler)
        done.wait()
        if box.get("err"):
            raise RuntimeError(f"Apple's high-quality model failed on frame {index}: {box['err']}")


class AppleNoiseSession(AppleSession):
    """Apple's AI temporal noise filter at the source size. It compares the current
    frame with the previous ones (no future frames, so it adds no delay), keeping
    a small ring of source buffers so earlier frames stay intact."""

    MAX_PREVIOUS = 2

    def __init__(self, sr, w, h, fmt, strength, via=None):
        self.sr, self.Q = sr, sr.Q
        self.via = via   # a normal format we fill/read, copied to/from fmt on the GPU
        self.w, self.h, self.out_w, self.out_h = w, h, w, h
        self.strength = float(strength)
        self.cfg = sr.t_config(w, h, fmt)
        try:
            self.prev = max(0, min(int(self.cfg.previousFrameCount()), self.MAX_PREVIOUS))
        except Exception:
            self.prev = 1
        self.proc = sr.Processor.alloc().init()
        t = time.monotonic()
        ok, err = sr.ok(self.proc.startSessionWithConfiguration_error_(self.cfg, None))
        if not ok:
            raise RuntimeError(f"Apple's noise filter wouldn't start for {w}x{h}: {err or 'no details given'}")
        self.load_seconds = time.monotonic() - t
        code = fourcc_int(fmt)
        _, self.layout = PIXEL_FORMATS[via or fmt]
        self.ring = [self._buffer(w, h, code, self.cfg.sourcePixelBufferAttributes())
                     for _ in range(self.prev + 1)]
        self.dst = self._buffer(w, h, code, self.cfg.destinationPixelBufferAttributes())
        if via:
            self.xfer = PixelTransfer()
            self.stage_in = self._buffer(w, h, fourcc_int(via), {})
            self.stage_out = self._buffer(w, h, fourcc_int(via), {})
        self._sync = self.proc.respondsToSelector_(b"processWithParameters:error:")
        self.k = 0

    def put(self, planes):
        slot = self.ring[self.k % len(self.ring)]
        if self.via:
            self.src = self.stage_in
            super().put(planes)
            self.xfer.copy(self.stage_in, slot)
        else:
            self.src = slot
            super().put(planes)

    def get(self, into):
        if not self.via:
            return super().get(into)
        self.xfer.copy(self.dst, self.stage_out)
        real, self.dst = self.dst, self.stage_out
        try:
            super().get(into)
        finally:
            self.dst = real

    def run(self, index):
        F, k, n = self.sr.Frame, self.k, len(self.ring)
        pts = lambda i: (i * 1001, 60000, 1, 0)
        s = F.alloc().initWithBuffer_presentationTimeStamp_(self.ring[k % n], pts(index))
        d = F.alloc().initWithBuffer_presentationTimeStamp_(self.dst, pts(index))
        prevs = [F.alloc().initWithBuffer_presentationTimeStamp_(self.ring[(k - j) % n], pts(index - j))
                 for j in range(min(self.prev, k), 0, -1)]   # oldest first
        params = self.sr.TParams.alloc().initWithSourceFrame_nextFrames_previousFrames_destinationFrame_filterStrength_hasDiscontinuity_(
            s, [], prevs, d, self.strength, k == 0)
        if params is None:
            raise RuntimeError("Apple refused the noise filter's frame settings")
        AppleQualitySession._process(self, params, index)
        self.k += 1


class FakeSR:
    """Stand-in for testing off a Mac: plain nearest-neighbour scaling, any factor."""

    def supported(self):
        return True

    def scale_factors(self, w, h):
        return [1.5, 2.0, 4.0]

    def session(self, w, h, factor, fmt):
        return FakeSession(w, h, factor, fmt)

    q_missing = None

    def q_supported(self):
        return True

    def q_scale_factors(self):
        return [4]

    q_formats = ["RGhA"]   # like the user's Air
    t_missing = None

    def t_supported(self):
        return True

    t_list = ["&8v0", "-8v0"]   # change in tests to try other formats

    def t_formats(self):
        return list(self.t_list)

    def t_session(self, w, h, fmt, strength, via=None):
        return FakeNoiseSession(w, h, via or fmt, strength)

    def q_pixel_formats(self, w, h, factor):
        return list(self.q_formats)

    def q_session(self, w, h, factor, fmt):
        return FakeQualitySession(w, h, factor, fmt)


class FakeSession:
    load_seconds = 0.0

    def __init__(self, w, h, factor, fmt):
        import numpy as np
        self.np = np
        self.w, self.h = w, h
        self.out_w, self.out_h = int(round(w * factor)), int(round(h * factor))
        _, self.layout = PIXEL_FORMATS[fmt]
        self.src = [np.zeros((r, b), np.uint8) for b, r in self.layout(w, h)]
        self.dst = [np.zeros((r, b), np.uint8) for b, r in self.layout(self.out_w, self.out_h)]
        ys = (np.arange(self.out_h) * h // self.out_h)
        xs = (np.arange(self.out_w) * w // self.out_w)
        uv_ys = (np.arange(self.out_h // 2) * (h // 2) // (self.out_h // 2))
        uv_xs = (np.arange(self.out_w // 2) * (w // 2) // (self.out_w // 2))
        self.idx = (ys, xs, uv_ys, uv_xs)

    def put(self, planes):
        for d, s in zip(self.src, planes):
            d[:] = s

    def run(self, index):
        ys, xs, uv_ys, uv_xs = self.idx
        if len(self.src) == 1:   # packed format, 8 bytes per pixel
            px = self.src[0].reshape(self.h, self.w, 8)
            self.dst[0][:] = px[ys][:, xs].reshape(self.dst[0].shape)
            return
        y, uv = self.src
        self.dst[0][:] = y[ys][:, xs]
        pairs = uv.reshape(uv.shape[0], -1, 2)[uv_ys][:, uv_xs]
        self.dst[1][:] = pairs.reshape(self.dst[1].shape)

    def get(self, into):
        for p, d in zip(into, self.dst):
            p[:] = d

    def stop(self):
        pass


class FakeNoiseSession(FakeSession):
    """Fake noise filter: passes the frame through unchanged (so tests can check
    that the format conversion round trip is clean)."""

    def __init__(self, w, h, fmt, strength):
        super().__init__(w, h, 1.0, fmt)
        self.last = None
        self.prev = 1

    def run(self, index):
        for d, s in zip(self.dst, self.src):
            d[:] = s


class FakeQualitySession(FakeSession):
    """Fake temporal model: same buffer juggling as the Apple one, and it checks
    that last frame's source is still intact when the next frame arrives."""

    def __init__(self, w, h, factor, fmt):
        super().__init__(w, h, factor, fmt)
        np = self.np
        self.srcs = [[np.zeros_like(p) for p in self.src] for _ in range(2)]
        self.k, self.last = 0, None

    def put(self, planes):
        for d, s in zip(self.srcs[self.k % 2], planes):
            d[:] = s

    def run(self, index):
        if self.k and self.last is not None:
            prev = self.srcs[(self.k - 1) % 2]
            if not all((a == b).all() for a, b in zip(prev, self.last)):
                raise RuntimeError("fake quality model: previous frame was overwritten")
        self.src = self.srcs[self.k % 2]
        super().run(index)
        self.last = [p.copy() for p in self.src]
        self.k += 1


# --------------------------------------------------------------------------- #
# AI modes
# --------------------------------------------------------------------------- #

class Single:
    """The whole frame through the model once (720p at 1.5x -> 1080p)."""

    name = "ai"
    label = "AI 1.5x"
    delay = 0

    def flush(self):
        return None

    def __init__(self, sr, w, h, factor, fmt):
        import numpy as np
        self.pix_fmt = PIXEL_FORMATS[fmt][0]
        self.s = sr.session(w, h, factor, fmt)
        self.out_w, self.out_h = self.s.out_w, self.s.out_h
        self.out = [np.zeros((r, b), np.uint8) for b, r in self.s.layout(self.out_w, self.out_h)]
        self.load_seconds = self.s.load_seconds
        self.describe = f"{w}x{h} -> {self.out_w}x{self.out_h} ({factor:g}x, whole frame)"

    def process(self, planes, index):
        self.s.put(planes)
        self.s.run(index)
        self.s.get(self.out)
        return self.out

    def stop(self):
        self.s.stop()


class Tiled2x:
    """
    Two overlapping half-frame tiles, each through the model at 2x, blended
    back together. 720p -> 1440p. The tiles overlap by at least 64 pixels and
    are cross-faded across the overlap so there's no visible seam.
    """

    name = "ai2x"
    label = "AI 2x tiles"

    delay = 0

    def __init__(self, sr, w, h, fmt, tile_w, parallel=False):
        import numpy as np
        self.np = np
        self.parallel = parallel
        self.pool = None
        if parallel:
            from concurrent.futures import ThreadPoolExecutor
            self.pool = ThreadPoolExecutor(max_workers=2, thread_name_prefix="tile")
        # Seconds spent per step, summed over frames (and over both tiles).
        self.steps = {"tiles copy in": 0.0, "tiles AI": 0.0, "tiles copy out": 0.0,
                      "tiles (wall time)": 0.0, "blend": 0.0}
        self.w, self.h, self.tw = w, h, tile_w
        self.pix_fmt = PIXEL_FORMATS[fmt][0]
        self.xr = w - tile_w                      # right tile's left edge in the source
        self.left = sr.session(tile_w, h, 2.0, fmt)
        self.right = sr.session(tile_w, h, 2.0, fmt)
        self.out_w, self.out_h = 2 * w, 2 * h
        lay = self.left.layout
        self.out = [np.zeros((r, b), np.uint8) for b, r in lay(self.out_w, self.out_h)]
        self.tiles = [[np.zeros((r, b), np.uint8) for b, r in lay(2 * tile_w, 2 * h)] for _ in range(2)]
        # Cross-fade weights across the overlap, in output pixels (0..256 fixed point).
        self.ox = 2 * self.xr                     # where the right tile starts in the output
        self.ov = 2 * tile_w - self.ox            # overlap width in the output
        ramp = np.linspace(256, 0, self.ov, dtype=np.float64).round().astype(np.uint16)
        pair_ramp = np.repeat(np.linspace(256, 0, self.ov // 2).round().astype(np.uint16), 2)
        self.wy, self.wuv = ramp, pair_ramp       # chroma: same weight for each U,V pair
        self.load_seconds = self.left.load_seconds + self.right.load_seconds
        self.describe = (f"{w}x{h} -> {self.out_w}x{self.out_h} (2x, two {tile_w}x{h} tiles, "
                         f"{2 * tile_w - w}px overlap)")

    def _tile(self, sess, x, tile, planes, index):
        a = time.monotonic()
        sess.put([p[:, x:x + self.tw] for p in planes])
        b = time.monotonic()
        sess.run(index)
        c = time.monotonic()
        sess.get(tile)
        d = time.monotonic()
        return b - a, c - b, d - c

    def process(self, planes, index):
        np = self.np
        jobs = ((self.left, 0, self.tiles[0]), (self.right, self.xr, self.tiles[1]))
        t0 = time.monotonic()
        if self.pool:
            futs = [self.pool.submit(self._tile, s, x, t, planes, index) for s, x, t in jobs]
            times = [f.result() for f in futs]
        else:
            times = [self._tile(s, x, t, planes, index) for s, x, t in jobs]
        t1 = time.monotonic()
        for key, i in (("tiles copy in", 0), ("tiles AI", 1), ("tiles copy out", 2)):
            self.steps[key] += sum(t[i] for t in times)
        self.steps["tiles (wall time)"] += t1 - t0
        ox, ov, end = self.ox, self.ov, 2 * self.tw
        for k, wts in ((0, self.wy), (1, self.wuv)):
            L, R, O = self.tiles[0][k], self.tiles[1][k], self.out[k]
            O[:, :ox] = L[:, :ox]
            O[:, end:] = R[:, ov:]
            mix = (L[:, ox:end].astype(np.uint16) * wts + R[:, :ov].astype(np.uint16) * (256 - wts) + 128) >> 8
            O[:, ox:end] = mix.astype(np.uint8)
        self.steps["blend"] += time.monotonic() - t1
        return self.out

    def flush(self):
        return None

    def stop(self):
        if self.pool:
            self.pool.shutdown(wait=True)
        self.left.stop()
        self.right.stop()


class Quality:
    """Apple's high-quality model (720p -> 1440p at 2x, or 2880p at 4x). It uses
    the previous frame too, so it has to see every frame in order."""

    name = "aiq"
    delay = 0

    def flush(self):
        return None

    def __init__(self, sr, w, h, fmt, factor=2, model_fmt=None, model_size=None):
        import numpy as np
        self.np = np
        mw, mh = model_size or (w, h)
        self.shrink = (mw, mh) != (w, h)
        if self.shrink:
            self.name = f"aiq{mh}"
            self.label = f"AI quality model {factor}x from {mh}p"
        else:
            self.label = f"AI quality model {factor}x"
        model_fmt = model_fmt or fmt
        self.convert = model_fmt != fmt or self.shrink   # convert/shrink each frame going in
        if self.shrink and model_fmt != "RGhA":
            raise RuntimeError("the smaller-input version needs the model's RGB format")
        self.pix_fmt = PIXEL_FORMATS[model_fmt][0]   # what comes out
        if self.convert:
            self.rgb = np.zeros((h, w, 4), np.float32 if self.shrink else np.float16)
        if self.shrink:
            self.ry, self.rx = resample_matrix(h, mh), resample_matrix(w, mw)
            self.small = np.zeros((mh, mw, 4), np.float16)
        self.convert_s = self.model_s = 0.0
        self.s = sr.q_session(mw, mh, factor, model_fmt)
        self.out_w, self.out_h = self.s.out_w, self.s.out_h
        self.out = [np.zeros((r, b), np.uint8) for b, r in self.s.layout(self.out_w, self.out_h)]
        self.load_seconds = self.s.load_seconds
        self.describe = (f"{w}x{h} -> {f'{mw}x{mh} -> ' if self.shrink else ''}{self.out_w}x{self.out_h} "
                         f"({factor}x, high-quality, uses the previous frame"
                         f"{', brought down to 4K afterwards' if self.out_w > TARGET_W else ''})")

    def process(self, planes, index):
        if self.convert:
            t = time.monotonic()
            nv12_to_rgbah(planes[0], planes[1], self.rgb)
            if self.shrink:
                self.small[:] = self.np.clip(shrink(self.rgb, self.ry, self.rx), 0, 1)
                planes = [self.small.view(self.np.uint8).reshape(self.small.shape[0], -1)]
            else:
                planes = [self.rgb.view(self.np.uint8).reshape(self.rgb.shape[0], -1)]
            self.convert_s += time.monotonic() - t
        self.s.put(planes)
        t = time.monotonic()
        self.s.run(index)
        self.model_s += time.monotonic() - t
        self.s.get(self.out)
        return self.out

    def stop(self):
        self.s.stop()


class Denoised2x:
    """Apple's AI temporal noise filter on the 720p picture, then the AI 2x tiles."""

    name = "tnr2x"
    label = "AI denoise + AI 2x tiles"

    def __init__(self, sr, w, h, fmt, tile_w, strength, parallel=False, pipeline=False):
        import numpy as np
        self.pipeline = pipeline
        # With the pipeline on, frame N is denoised in the background while frame
        # N-1 is upscaled, so each call returns the previous frame's result.
        self.delay = 1 if pipeline else 0
        self.pool = None
        if pipeline:
            from concurrent.futures import ThreadPoolExecutor
            self.pool = ThreadPoolExecutor(max_workers=1, thread_name_prefix="denoise")
        self.pending = None
        offered = sr.t_formats()
        via = None
        nf = fmt if (not offered or fmt in offered) else next((f for f in NOISE_FORMATS if f in offered), None)
        if nf is None:
            nf = next((f for f in COMPRESSED_420V if f in offered), None)
            via = fmt if nf else None
        if nf is None:
            raise RuntimeError(f"the noise filter needs pixel formats {offered}, which the benchmark can't make yet")
        self.noise_fmt = nf
        self.bridge = FormatBridge(via or nf, w, h)
        self.d = sr.t_session(w, h, nf, strength, via)
        self.up = Tiled2x(sr, w, h, fmt, tile_w, parallel)
        self.pix_fmt, self.out_w, self.out_h = self.up.pix_fmt, self.up.out_w, self.up.out_h
        self.raw = [np.zeros((r, b), np.uint8) for b, r in self.d.layout(w, h)]
        # Two clean buffers, so the pipeline can fill one while the other is upscaled.
        self.cleans = [[np.zeros((r, b), np.uint8) for b, r in PIXEL_FORMATS[fmt][1](w, h)] for _ in range(2)]
        self.k = 0
        self.dsteps = {"denoise copy in": 0.0, "noise filter": 0.0, "denoise copy out": 0.0}
        self.load_seconds = self.d.load_seconds + self.up.load_seconds
        prev = getattr(self.d, "prev", 1)
        conv = (f", copied to Apple's compressed {nf} format on the GPU" if via else
                f", converted to {nf}" if nf != fmt else "")
        self.describe = (f"noise filter (strength {strength:g}, {prev} previous "
                         f"frame{'s' if prev != 1 else ''}{conv}), then {self.up.describe}")
        self.denoise_s = self.convert_s = 0.0

    def _denoise(self, planes, index, clean):
        t = time.monotonic()
        src = self.bridge.to(planes)
        t1 = time.monotonic()
        self.d.put(src)
        t2 = time.monotonic()
        self.d.run(index)
        t3 = time.monotonic()
        self.d.get(self.raw)
        t4 = time.monotonic()
        self.bridge.back(self.raw, clean)
        t5 = time.monotonic()
        self.denoise_s += t4 - t1
        self.convert_s += (t1 - t) + (t5 - t4)
        self.dsteps["denoise copy in"] += t2 - t
        self.dsteps["noise filter"] += t3 - t2
        self.dsteps["denoise copy out"] += t5 - t3
        return clean

    def process(self, planes, index):
        clean = self.cleans[self.k % 2]
        self.k += 1
        if not self.pipeline:
            return self.up.process(self._denoise(planes, index, clean), index)
        fut = self.pool.submit(self._denoise, planes, index, clean)
        out = None
        if self.pending is not None:
            out = self.up.process(*self.pending)   # previous frame, while this one denoises
        self.pending = (fut.result(), index)
        return out

    def flush(self):
        """The last frame still waiting in the pipeline, if any."""
        if self.pending is None:
            return None
        out = self.up.process(*self.pending)
        self.pending = None
        return out

    def stop(self):
        if self.pool:
            self.pool.shutdown(wait=True)
        self.d.stop()
        self.up.stop()


def pick_tile_width(sr, w, h):
    for tw in TILE_WIDTHS:
        if 2 * tw - w < 64 or tw > w:
            continue
        f = sr.scale_factors(tw, h)
        if isinstance(f, list) and any(abs(x - 2.0) < 0.01 for x in f):
            return tw
    return None


# --------------------------------------------------------------------------- #
# Helpers
# --------------------------------------------------------------------------- #

def need(tool):
    if not shutil.which(tool):
        sys.exit(f"{tool} not found. Install it with: brew install ffmpeg")


def probe(clip):
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
                          "stream=width,height,field_order,r_frame_rate,codec_name",
                          "-of", "json", str(clip)], capture_output=True, text=True, check=True)
    s = json.loads(out.stdout)["streams"][0]
    s["interlaced"] = s.get("field_order", "progressive") not in ("progressive", "unknown")
    return s


def read_exact(pipe, n):
    buf = bytearray(n)
    view, got = memoryview(buf), 0
    while got < n:
        k = pipe.readinto(view[got:])
        if not k:
            return None
        got += k
    return buf


# How the comparison files are written; changed by --bitrate and --out-height.
ENCODE = {"mbps": 25.0, "w": TARGET_W, "h": TARGET_H}


def out_name(clip, label):
    """clip_720p + ai2x -> clip_720p_ai2x_4k.mp4 (or ..._ai2x_40M_1440p.mp4 for other settings)."""
    rate = "" if ENCODE["mbps"] == 25 else f"_{ENCODE['mbps']:g}M"
    size = "4k" if ENCODE["h"] == TARGET_H else f"{ENCODE['h']}p"
    return clip.with_name(f"{clip.stem}_{label}{rate}_{size}.mp4")


def encoder(out_path, w, h, pix_fmt, clip, limit, vcodec, pre_filter=""):
    tag = ["-tag:v", "hvc1"] if "hevc" in vcodec else []
    colour = (YUV_TAGS if pix_fmt == "nv12" else "out_color_matrix=bt709") + ":out_range=tv"
    vf = f"{pre_filter + ',' if pre_filter else ''}scale={ENCODE['w']}:{ENCODE['h']}:flags=lanczos:{colour}"
    return subprocess.Popen(
        ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
         "-f", "rawvideo", "-pix_fmt", pix_fmt, "-s", f"{w}x{h}", "-r", "60000/1001", "-i", "pipe:0",
         *limit, "-i", str(clip), "-map", "0:v", "-map", "1:a?", "-c:a", "copy",
         "-vf", vf, "-pix_fmt", "yuv420p", "-colorspace", "bt709", "-color_primaries", "bt709",
         "-color_trc", "bt709", "-color_range", "tv", "-c:v", vcodec, "-b:v", f"{ENCODE['mbps']:g}M", *tag, str(out_path)],
        stdin=subprocess.PIPE)


FONTS = ("/System/Library/Fonts/Supplemental/Arial.ttf", "/System/Library/Fonts/Helvetica.ttc",
         "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf")
PANEL_W, PANEL_H = 1280, 720   # size of each snapshot panel, cut from the 4K picture


def make_snapshot(rows, out_path, crop_xy):
    """
    rows: list of rows; each row is a list of panels, where a panel is
    (label, raw_bytes, width, height, pre_filter, pix_fmt) or None for an empty spot.
    Every panel is scaled to 4K exactly like its video file, cropped to the same
    area, labelled, and placed in a grid; empty spots are black.
    """
    import tempfile
    font = next((f for f in FONTS if Path(f).exists()), None)
    x, y = crop_xy
    with tempfile.TemporaryDirectory() as tmp:
        inputs, chains = [], []   # chains: (filter chain, label, grid x, grid y)
        for r, row in enumerate(rows):
            for c, panel in enumerate(row):
                if panel is None:
                    continue
                label, raw, w, h, pre, pf = panel
                f = Path(tmp) / f"p{len(chains)}.raw"
                f.write_bytes(raw)
                inputs += ["-f", "rawvideo", "-pix_fmt", pf, "-s", f"{w}x{h}", "-i", str(f)]
                if pf == "nv12":
                    # Same path as the video files: scale in YUV, then to RGB as BT.709.
                    chain = (f"{pre + ',' if pre else ''}scale={TARGET_W}:{TARGET_H}:flags=lanczos,"
                             f"crop={PANEL_W}:{PANEL_H}:{x}:{y},scale={YUV_TAGS}:out_range=pc,format=rgb24")
                else:
                    chain = (f"{pre + ',' if pre else ''}scale={TARGET_W}:{TARGET_H}:flags=lanczos,"
                             f"crop={PANEL_W}:{PANEL_H}:{x}:{y},format=rgb24")
                chains.append((chain, label, c * PANEL_W, r * PANEL_H))
        if not chains:
            print("  Couldn't make the snapshot: nothing to show")
            return False
        cols = max(len(row) for row in rows)
        grid_w, grid_h = cols * PANEL_W, len(rows) * PANEL_H

        def build(with_labels):
            parts = []
            for i, (chain, label, _, _) in enumerate(chains):
                text = ""
                if with_labels and font:
                    safe = label.replace(":", r"\:").replace("'", "")
                    text = (f",drawtext=fontfile='{font}':text='{safe}':x=20:y=20:fontsize=40:"
                            f"fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=12")
                parts.append(f"[{i}:v]{chain}{text}[p{i}]")
            # A black canvas the size of the whole grid, with each panel laid on top.
            parts.append(f"color=black:s={grid_w}x{grid_h}:d=1,format=rgb24[c0]")
            for i, (_, _, gx, gy) in enumerate(chains):
                parts.append(f"[c{i}][p{i}]overlay={gx}:{gy}:eof_action=repeat:format=rgb[c{i + 1}]")
            parts[-1] = parts[-1].rsplit("[", 1)[0] + "[out]"
            return ";".join(parts)

        for with_labels in (True, False):
            r = subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", *inputs,
                                "-filter_complex", build(with_labels), "-map", "[out]",
                                "-frames:v", "1", str(out_path)], capture_output=True, text=True)
            if r.returncode == 0:
                return True
        print(f"  Couldn't make the snapshot: {r.stderr.strip()[-300:]}")
        return False


# --------------------------------------------------------------------------- #
# Commands
# --------------------------------------------------------------------------- #

def cmd_info(args):
    print(f"macOS {platform.mac_ver()[0] or '?'} on {platform.machine()}, Python {platform.python_version()}")
    if not args.verbose:
        quiet_apple_logs()
    sr = AppleSR()
    print(f"Real-time super resolution supported: {sr.supported()}")
    for w, h in ((1280, 720), (1920, 1080)):
        print(f"  {w}x{h}: scale factors {sr.scale_factors(w, h)}")
    for k, v in sr.limits().items():
        print(f"  {k}: {v}")
    for w, h, f in ((1280, 720, 1.5), (1280, 720, 2.0), (1920, 1080, 2.0)):
        try:
            cfg = sr.config(w, h, f)
            print(f"  {w}x{h} at {f:g}x: OK, pixel formats {sr.pixel_formats(cfg)}")
        except Exception as e:
            print(f"  {w}x{h} at {f:g}x: not available ({e})")
    tw = pick_tile_width(sr, 1280, 720)
    print(f"  720p in two tiles at 2x: {'tiles of ' + str(tw) + 'x720' if tw else 'not available'}")

    print(f"\nAI temporal noise filter supported: {sr.t_supported()}"
          f"{' (' + sr.t_missing + ')' if sr.t_missing else ''}")
    if sr.t_supported():
        for k, v in sr.t_info(1280, 720, "420v").items():
            print(f"  {k}: {v}")

    print(f"\nHigh-quality super resolution supported: {sr.q_supported()}"
          f"{' (' + sr.q_missing + ')' if sr.q_missing else ''}")
    if sr.q_supported():
        print(f"  scale factors: {sr.q_scale_factors()}")
        try:
            print(f"  default revision: {int(sr.QConfig.defaultRevision())}, "
                  f"supported: {[int(x) for x in sr.QConfig.supportedRevisions()]}")
        except Exception as e:
            print(f"  revisions: error: {e}")
        names = {0: "needs downloading", 1: "downloading", 2: "ready"}
        for qw, qh, qf in ((1280, 720, 2), (1280, 720, 4), (960, 540, 4)):
            try:
                cfg = sr.q_config(qw, qh, qf)
                status = sr.q_status(cfg)
                print(f"  {qw}x{qh} at {qf}x: OK, pixel formats {sr.pixel_formats(cfg)}, "
                      f"model status {status} ({names.get(status, 'unknown')})")
            except Exception as e:
                print(f"  {qw}x{qh} at {qf}x: not available ({e})")


def parse_q_enums(text):
    for item in (text or "").split(","):
        if not item.strip():
            continue
        k, _, v = item.partition("=")
        if k.strip() not in Q_ENUMS:
            sys.exit(f"--q-enums: unknown name {k!r}; known: {', '.join(Q_ENUMS)}")
        Q_ENUMS[k.strip()] = int(v)


def split_filters(chain):
    """Split a simple filter chain at its commas (not inside quotes or brackets)."""
    if any(ch in chain for ch in "'\"[]\\"):
        return [chain]
    return [p for p in chain.split(",") if p.strip()]


def cleanup_speed(clip, chain):
    """Time the cleanup filters on 5 seconds of the clip, on the CPU like ffmpeg would live."""
    import re
    tests = [("decoding alone", "null"), ("decoding + full cleanup", chain)]
    parts = split_filters(chain)
    if len(parts) > 1:
        tests += [(f"decoding + {p.split('=')[0]} only", p) for p in parts]
    results = []
    for label, vf in tests:
        t = time.monotonic()
        r = subprocess.run(["ffmpeg", "-hide_banner", "-nostats", "-loglevel", "error", "-stats",
                            "-export_side_data", "venc_params", "-t", "5", "-i", str(clip),
                            "-map", "0:v:0", "-vf", vf, "-f", "null", "-"],
                           capture_output=True, text=True)
        secs = time.monotonic() - t
        frames = re.findall(r"frame=\s*(\d+)", r.stderr)
        if r.returncode != 0 or not frames:
            results.append((label, None))
            continue
        results.append((label, int(frames[-1]) / secs if secs else 0))
    return results


MODE_ORDER = ("ai", "ai2x", "tnr2x", "aiq", "aiq540")
SHARPENED = ("ai2x", "tnr2x")      # modes that also get a sharpened version
COLUMNS = ("plain", "ai", "ai2x", "ai2x_sharp", "tnr2x", "tnr2x_sharp", "aiq", "aiq540")


def read_log(f):
    try:
        f.seek(0)
        return f.read().decode(errors="replace").strip()[-500:]
    except Exception:
        return ""


def cmd_run(args):
    ENCODE.update(mbps=args.bitrate, h=args.out_height, w=args.out_height * 16 // 9)
    need("ffmpeg"); need("ffprobe")
    try:
        import numpy as np
    except ImportError:
        sys.exit("numpy isn't installed. Run:  pip3 install pyobjc numpy")
    parse_q_enums(args.q_enums)
    clip = Path(args.clip).expanduser()
    if not clip.exists():
        sys.exit(f"No such file: {clip}")
    info = probe(clip)
    w, h = int(info["width"]), int(info["height"])
    if (w, h) != (1280, 720):
        sys.exit(f"Clip is {w}x{h}. Your Mac's AI model only takes 720p for now; "
                 f"record one with:  python3 record_clips.py --only 720p")
    if args.snapshot is not None and args.seconds and args.snapshot >= args.seconds:
        sys.exit(f"--snapshot {args.snapshot:g} is past the end of --seconds {args.seconds:g}")

    apple = not args.fake
    if apple and not args.verbose:
        quiet_apple_logs()
    sr = AppleSR() if apple else FakeSR()
    if not sr.supported():
        sys.exit("This Mac reports that real-time super resolution isn't supported.")

    fmt = "420v"
    if apple:
        formats = sr.pixel_formats(sr.config(w, h, 1.5))
        fmt = next((f for f in ("420v", "420f") if f in formats), None)
        if not fmt:
            sys.exit(f"No usable pixel format; the model accepts {formats}")
    pix_fmt, layout = PIXEL_FORMATS[fmt]

    wanted = [m.strip() for m in args.modes.split(",") if m.strip()]
    for m in wanted:
        if m not in MODE_ORDER:
            sys.exit(f"Unknown mode {m!r}; choose from {', '.join(MODE_ORDER)}")
    wanted = [m for m in MODE_ORDER if m in wanted]

    def build(kind, quiet=False):
        say = (lambda *_: None) if quiet else print
        if kind == "ai":
            return Single(sr, w, h, 1.5, fmt)
        if kind == "ai2x":
            tw = pick_tile_width(sr, w, h)
            if tw:
                return Tiled2x(sr, w, h, fmt, tw, args.parallel_tiles)
            say("  ai2x  skipped: this Mac doesn't allow 2x on half-frame tiles")
            return None
        if kind == "tnr2x":
            tw = pick_tile_width(sr, w, h)
            if not tw:
                say("  tnr2x skipped: this Mac doesn't allow 2x on half-frame tiles")
                return None
            if not sr.t_supported():
                say(f"  tnr2x skipped: this Mac doesn't offer the AI noise filter"
                    f"{' (' + sr.t_missing + ')' if sr.t_missing else ''}")
                return None
            try:
                return Denoised2x(sr, w, h, fmt, tw, args.denoise, args.parallel_tiles, args.pipeline)
            except Exception as e:
                say(f"  tnr2x skipped: {e}")
                return None
        if kind in ("aiq", "aiq540"):
            model_size = (960, 540) if kind == "aiq540" else None
            if not sr.q_supported():
                say(f"  {kind:5} skipped: this Mac doesn't offer the high-quality model"
                    f"{' (' + sr.q_missing + ')' if sr.q_missing else ''}")
                return None
            factors = sr.q_scale_factors()
            factor = 2
            if model_size:
                factor = 4
                if isinstance(factors, list) and 4 not in factors:
                    say(f"  aiq540 skipped: the high-quality model doesn't do 4x here ({factors})")
                    return None
            elif isinstance(factors, list):
                usable = sorted(f for f in factors if f >= 2)
                if not usable:
                    say(f"  aiq   skipped: the high-quality model doesn't upscale here ({factors})")
                    return None
                factor = 2 if 2 in usable else usable[0]
            try:
                qf = sr.q_pixel_formats(*(model_size or (w, h)), factor)
            except Exception as e:
                say(f"  {kind:5} skipped: {e}")
                return None
            model_fmt = fmt
            if qf and fmt not in qf:
                model_fmt = next((f for f in qf if f in PIXEL_FORMATS), None)
                if not model_fmt:
                    say(f"  {kind:5} skipped: it needs pixel formats {qf}, which the benchmark can't make yet")
                    return None
            if model_size and model_fmt != "RGhA":
                if qf and "RGhA" not in qf:
                    say(f"  aiq540 skipped: needs the model's RGB format, it offers {qf}")
                    return None
                model_fmt = "RGhA"
            try:
                return Quality(sr, w, h, fmt, factor, model_fmt, model_size)
            except Exception as e:
                say(f"  {kind:5} skipped: {e}")
                return None

    print(f"Clip: {clip.name}, 720p {info['codec_name']}")
    modes = [m for m in (build(k) for k in wanted) if m]
    if not modes:
        sys.exit("No AI modes to run.")
    for m in modes:
        print(f"  {m.name:6} {m.describe}, model loaded in {m.load_seconds:.1f}s")

    # Cleanup row of the snapshot: a second decoder with the cleanup filters, plus
    # a second copy of each model (so the quality model's memory of earlier frames
    # isn't mixed between the two rows).
    cleanup = None if (args.no_cleanup or args.snapshot is None) else args.cleanup_filter
    twins = {}
    if cleanup:
        for m in modes:
            t = build(m.name, quiet=True)
            if t:
                twins[m.name] = t
        print(f"  cleanup row: {cleanup}  (second copy of each model loaded)")

    # With --no-output and --snapshot, skip ahead to a little before the snapshot.
    # Both decoders read from the start and drop the same frames after the frame-rate
    # step, so their frames line up exactly with each other and with a full run.
    # (Seeking with -ss would be quicker but can land a frame off.) The cleanup
    # filters come after the skip, so they only work on the frames that are used.
    snap_frame = int(round(args.snapshot * REALTIME_FPS)) if args.snapshot is not None else None
    skip_frames = 0
    if snap_frame is not None and args.no_output:
        skip_frames = max(0, snap_frame - WARMUP_FRAMES)
    limit = ["-t", f"{args.seconds:g}"] if args.seconds else []
    snap_at = snap_frame - skip_frames if snap_frame is not None else None
    warm_from = snap_at - WARMUP_FRAMES if snap_at is not None else None

    import tempfile
    dec_logs = []

    def start_decoder(post_filter):
        vf = "fps=60000/1001"
        if skip_frames:
            vf += f",trim=start_frame={skip_frames},setpts=PTS-STARTPTS"
        if post_filter:
            vf += f",{post_filter}"
        vf += f",format={pix_fmt}"
        log = tempfile.TemporaryFile()
        dec_logs.append(log)
        return subprocess.Popen(
            ["ffmpeg", "-hide_banner", "-loglevel", "error", "-export_side_data", "venc_params",
             *limit, "-i", str(clip), "-map", "0:v:0", "-vf", vf, "-f", "rawvideo", "pipe:1"],
            # Messages go to a log that's shown only if the decoder fails, so the
            # "broken pipe" note when we stop it on purpose stays hidden.
            stdout=subprocess.PIPE, stderr=log)

    decoder = start_decoder("")
    clean_dec = start_decoder(cleanup) if cleanup else None

    outputs = {}   # mode name -> list of (encoder process, path)
    plain_path = out_name(clip, "plain")
    if not args.no_output:
        if not args.no_compare:
            # Made from the very same decoded frames as the AI files, so all the
            # 4K files line up frame for frame.
            outputs["plain"] = [(encoder(plain_path, w, h, pix_fmt, clip, limit, args.encoder), plain_path)]
        for m in modes:
            path = out_name(clip, m.name)
            outputs[m.name] = [(encoder(path, m.out_w, m.out_h, m.pix_fmt, clip, limit, args.encoder), path)]
            if m.name in SHARPENED and args.sharpness > 0:
                sp = out_name(clip, f"{m.name}_sharp")
                outputs[m.name].append((encoder(sp, m.out_w, m.out_h, m.pix_fmt, clip, limit, args.encoder,
                                                pre_filter=f"cas=strength={args.sharpness}"), sp))

    finished = set()
    snaps = {}   # (row, column name) -> panel
    crop_xy = ((TARGET_W - PANEL_W) // 2, (TARGET_H - PANEL_H) // 2)
    if args.crop:
        try:
            cx, cy = (int(v) for v in args.crop.split(","))
            crop_xy = (max(0, min(cx, TARGET_W - PANEL_W)), max(0, min(cy, TARGET_H - PANEL_H)))
        except ValueError:
            sys.exit("--crop needs two numbers, like  --crop 1280,720")
    sharp = f"cas=strength={args.sharpness}" if args.sharpness > 0 else ""

    def take_snapshot(row, prefix, raw, outs):
        if raw is not None:
            snaps[(row, "plain")] = (f"{prefix}Plain scaling (no AI)", bytes(raw), w, h, "", pix_fmt)
        for m, data in outs:
            snaps[(row, m.name)] = (f"{prefix}{m.label}", data, m.out_w, m.out_h, "", m.pix_fmt)
            if m.name in SHARPENED and sharp:
                snaps[(row, f"{m.name}_sharp")] = (f"{prefix}{m.label} + sharpen {args.sharpness:g}",
                                                   data, m.out_w, m.out_h, sharp, m.pix_fmt)

    def to_planes(raw):
        planes, pos = [], 0
        for b, r in layout(w, h):
            planes.append(np.frombuffer(raw, np.uint8, b * r, pos).reshape(r, b))
            pos += b * r
        return planes

    in_size = sum(b * r for b, r in layout(w, h))
    max_delay = max([m.delay for m in modes] + [t.delay for t in twins.values()])

    def write_out(m, out):
        live = [(proc, path) for proc, path in outputs.get(m.name, []) if path not in finished]
        if live:
            data = b"".join(p.tobytes() for p in out)
            for proc, path in live:
                try:
                    proc.stdin.write(data)
                except (BrokenPipeError, OSError):
                    finished.add(path)   # file ended a moment early (audio ran out first)
    ai_time = {m.name: 0.0 for m in modes}
    window = {m.name: 0.0 for m in modes}
    other_time, n, start = 0.0, 0, time.monotonic()
    window_start, window_n = start, 0
    head = "".join(f"   {m.name} ms/frame" for m in modes)
    peaks = {m.name: [] for m in modes}   # per-10s averages, to show warming up
    if skip_frames:
        print(f"\nSkipping to {skip_frames / REALTIME_FPS:.1f}s; running {snap_at} frames to settle, "
              f"then the snapshot frame.")
    print(f"\nseconds  frames   fps (last 10s){head}")
    try:
        while True:
            t0 = time.monotonic()
            raw = read_exact(decoder.stdout, in_size)
            if raw is None:
                break
            clean_raw = None
            if clean_dec is not None:
                clean_raw = read_exact(clean_dec.stdout, in_size)
                if clean_raw is None:
                    print("  The cleanup filters stopped early; leaving the cleanup row out.")
                    print("  " + read_log(dec_logs[1]))
                    clean_dec.kill(); clean_dec = None
                    for t in twins.values():
                        t.stop()
                    twins = {}
            planes = to_planes(raw)
            t1 = time.monotonic()
            other_time += t1 - t0
            for proc, path in outputs.get("plain", []):
                if path not in finished:
                    try:
                        proc.stdin.write(raw)
                    except (BrokenPipeError, OSError):
                        finished.add(path)
            snap_outs = []
            for m in modes:
                a = time.monotonic()
                out = m.process(planes, n)
                d = time.monotonic() - a
                ai_time[m.name] += d
                window[m.name] += d
                b = time.monotonic()
                if out is not None:   # None: a pipelined mode's first call, nothing ready yet
                    write_out(m, out)
                other_time += time.monotonic() - b
                if out is not None and n - m.delay == snap_at:
                    snap_outs.append((m, b"".join(p.tobytes() for p in out)))
            if n == snap_at or snap_outs:
                take_snapshot(0, "", raw if n == snap_at else None, snap_outs)
            # The cleanup copies only need to run for the frames leading up to the snapshot.
            if clean_raw is not None and twins and warm_from <= n <= snap_at + max_delay:
                cplanes = to_planes(clean_raw)
                couts = []
                for name, t in twins.items():
                    out = t.process(cplanes, n)
                    if out is not None and n - t.delay == snap_at:
                        couts.append((t, b"".join(p.tobytes() for p in out)))
                if n == snap_at or couts:
                    take_snapshot(1, "Cleanup + ", clean_raw if n == snap_at else None, couts)
            if clean_dec is not None and snap_at is not None and n >= snap_at + max_delay:
                clean_dec.stdout.close(); clean_dec.kill(); clean_dec.wait(); clean_dec = None
            n += 1
            if snap_at is not None and n > snap_at + max_delay and args.no_output:
                break   # snapshot taken, nothing else to write
            window_n += 1
            now = time.monotonic()
            if now - window_start >= 10:
                for m in modes:
                    peaks[m.name].append(1000 * window[m.name] / window_n)
                cols = "".join(f"   {1000 * window[m.name] / window_n:{len(m.name) + 9}.1f}" for m in modes)
                print(f"{now - start:7.0f}  {n:6d}   {window_n / (now - window_start):8.1f}      {cols}",
                      flush=True)
                window = {m.name: 0.0 for m in modes}
                window_start, window_n = now, 0
        # Pipelined modes still hold their last frame; write it so files stay complete.
        for m in modes:
            out = m.flush()
            if out is not None:
                write_out(m, out)
    except KeyboardInterrupt:
        print("\nStopped early.")
    finally:
        for m in list(modes) + list(twins.values()):
            m.stop()
        for dec in (decoder, clean_dec):
            if dec is not None:
                dec.stdout.close(); dec.kill(); dec.wait()
        for procs in outputs.values():
            for proc, _ in procs:
                try:
                    proc.stdin.close()
                except (BrokenPipeError, OSError):
                    pass
                proc.wait()
    failed = {path for procs in outputs.values() for proc, path in procs if proc.returncode}

    total = time.monotonic() - start
    if not n:
        sys.exit(f"No frames were processed. {read_log(dec_logs[0])}")
    print(f"\nProcessed {n} frames in {total:.1f}s ({n / total:.1f} fps overall"
          f"{', slower while writing files' if outputs else ''}"
          f"{', including the cleanup copies' if twins else ''})")
    if n < 300 and args.snapshot is not None:
        print(f"(Only {n} frames, so treat these speeds as rough. A speed test without "
              f"--snapshot gives steadier numbers.)")
    print(f"Live TV allows {FRAME_BUDGET_MS:.1f} ms per frame:")
    for m in modes:
        ms = 1000 * ai_time[m.name] / n
        verdict = ("fast enough" if ms <= FRAME_BUDGET_MS * 0.9 else
                   "borderline" if ms <= FRAME_BUDGET_MS * 1.05 else "too slow")
        print(f"  {m.name:6} {ms:6.1f} ms per frame ({1000 / ms if ms else 0:5.0f} fps): {verdict} for live TV")
        if len(peaks[m.name]) >= 3 and peaks[m.name][0] > 0:
            first, last = peaks[m.name][0], peaks[m.name][-1]
            print(f"         first 10 s {first:.1f} ms, last 10 s {last:.1f} ms "
                  f"({100 * (last - first) / first:+.0f}%: "
                  f"{'slowing as the Mac warms up' if last > first * 1.1 else 'steady'})")
        steps = {}
        if isinstance(m, Denoised2x):
            steps.update(m.dsteps)
            steps.update(m.up.steps)
        elif isinstance(m, Tiled2x):
            steps.update(m.steps)
        if steps:
            print("         where the time goes (ms per frame):")
            for k, v in steps.items():
                print(f"           {k:22} {1000 * v / n:5.1f}")
            notes = []
            if getattr(m, "parallel", False) or getattr(getattr(m, "up", None), "parallel", False):
                notes.append("the two tiles run at the same time, so 'tiles (wall time)' is what counts")
            if getattr(m, "pipeline", False):
                notes.append("denoising overlaps with upscaling the previous frame, so the steps add "
                             "up to more than the time per frame")
            for note in notes:
                print(f"           ({note})")
        if getattr(m, "convert", False):
            print(f"         of which the model itself: {1000 * m.model_s / n:.1f} ms; converting"
                  f"{' and shrinking' if m.shrink else ''} its input: {1000 * m.convert_s / n:.1f} ms "
                  f"(done slowly on the CPU here; a live version would do it on the GPU)")

    if outputs and not args.no_compare:
        orig = clip.with_name(f"{clip.stem}_original_720p.ts")
        subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", *limit, "-i", str(clip),
                        "-map", "0:v:0", "-map", "0:a?", "-c", "copy", str(orig)], check=False)
        print("\nFiles to compare on the TV:")
        print(f"  {orig.name:34} the broadcast as-is")
        print(f"  {plain_path.name:34} normal scaling to 4K, no AI")
        for m in modes:
            for _, path in outputs[m.name]:
                note = {"ai": "AI 1.5x, then stretched to 4K",
                        "ai2x": "AI 2x in two tiles, then stretched to 4K",
                        "tnr2x": "AI noise filter, then AI 2x tiles, then to 4K",
                        "aiq": f"{m.label}, then brought to 4K",
                        "aiq540": f"{m.label}, exactly 4K"}[m.name]
                if "sharp" in path.name:
                    note = f"{m.label} plus light sharpening ({args.sharpness:g})"
                if path in failed:
                    note = "FAILED: ffmpeg stopped while writing this file (often low memory)"
                print(f"  {path.name:34} {note}")

    if snap_at is not None:
        if not snaps:
            print(f"\nNo snapshot: the clip ended before {args.snapshot:g} seconds.")
        else:
            shot = clip.with_name(f"{clip.stem}_compare.png")
            columns = [c for c in COLUMNS
                       if any((r, c) in snaps for r in (0, 1))]
            rows = [[snaps.get((r, c)) for c in columns] for r in (0, 1) if any((r, c) in snaps for c in columns)]
            if make_snapshot(rows, shot, crop_xy):
                print(f"\nSnapshot at {args.snapshot:g}s: {shot.name}  "
                      f"({len(columns)} columns x {len(rows)} row{'s' if len(rows) > 1 else ''}; area {PANEL_W}x{PANEL_H} "
                      f"at {crop_xy[0]},{crop_xy[1]})")
                for r, row in enumerate(rows):
                    print(f"  {'top' if r == 0 else 'bottom'} row: {', '.join(p[0] for p in row if p)}")

    if cleanup and snaps:
        print("\nCleanup speed on the CPU (5 seconds of the clip; live TV needs 60 fps):")
        for label, fps in cleanup_speed(clip, cleanup):
            print(f"  {label:34} {'failed' if fps is None else f'{fps:6.0f} fps'}")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="command", required=True)
    i = sub.add_parser("info", help="show what this Mac supports")
    i.add_argument("--verbose", action="store_true", help="show Apple's model-setup notes too")
    r = sub.add_parser("run", help="benchmark a clip")
    r.add_argument("clip")
    r.add_argument("--seconds", type=float, default=None)
    r.add_argument("--no-output", action="store_true")
    r.add_argument("--no-compare", action="store_true")
    r.add_argument("--modes", default="ai2x,tnr2x,aiq,aiq540")
    r.add_argument("--denoise", type=float, default=0.5)
    r.add_argument("--parallel-tiles", action="store_true")
    r.add_argument("--pipeline", action="store_true")
    r.add_argument("--sharpness", type=float, default=0.4)
    r.add_argument("--snapshot", type=float, default=None)
    r.add_argument("--crop", default=None)
    r.add_argument("--encoder", default="hevc_videotoolbox")
    r.add_argument("--bitrate", type=float, default=25.0, help="Mbps for the comparison files (default 25)")
    r.add_argument("--out-height", type=int, default=TARGET_H, choices=[1080, 1440, 2160],
                   help="height of the comparison files (default 2160 = 4K)")
    r.add_argument("--no-cleanup", action="store_true")
    r.add_argument("--cleanup-filter", default=DEFAULT_CLEANUP)
    r.add_argument("--q-enums", default="", help=argparse.SUPPRESS)
    r.add_argument("--verbose", action="store_true", help="show Apple's model-setup notes too")
    r.add_argument("--fake", action="store_true", help=argparse.SUPPRESS)
    args = p.parse_args()
    {"info": cmd_info, "run": cmd_run}[args.command](args)


if __name__ == "__main__":
    main()
