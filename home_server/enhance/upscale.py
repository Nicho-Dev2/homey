"""Step 2 of the enhancer: 2x AI upscaling (720p -> 1440p, 1080p -> 4K).

Uses Apple's real-time super-resolution model (VideoToolbox, on the Mac's GPU).
The model only takes small pictures at 2x, so the frame goes through as overlapping
pieces ("tiles": two for 720p, a 3 x 2 grid for 1080p) that are cross-faded back together.

Frames are NV12: a brightness plane (Y) and a half-height color plane (UV).
"""
import os
import threading
import numpy as np
from common import FAKE

NV12 = int.from_bytes(b"420v", "big")                              # CoreVideo's name for video-range NV12


def quiet_apple_logs():
    """Drop Apple's long model-setup notes from this process's messages; pass everything else on."""
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


class _Apple:
    """The VideoToolbox classes, reached through PyObjC."""

    def __init__(self):
        import objc
        import Quartz                      # CoreVideo's pixel-buffer functions live here in PyObjC
        self.Q = Quartz
        self.pool = objc.autorelease_pool        # frees Apple's temporary objects when a block of calls ends
        objc.loadBundle("VideoToolbox", bundle_path="/System/Library/Frameworks/VideoToolbox.framework",
                        module_globals={})
        self.Config = objc.lookUpClass("VTLowLatencySuperResolutionScalerConfiguration")
        self.Params = objc.lookUpClass("VTLowLatencySuperResolutionScalerParameters")
        self.Processor = objc.lookUpClass("VTFrameProcessor")
        self.Frame = objc.lookUpClass("VTFrameProcessorFrame")
        # PyObjC doesn't know these newer methods; tell it the last argument is an error to hand back.
        for sel in (b"startSessionWithConfiguration:error:", b"processWithParameters:error:"):
            try:
                objc.registerMetaDataForSelector(
                    b"VTFrameProcessor", sel,
                    {"retval": {"type": objc._C_NSBOOL},
                     "arguments": {3: {"type_modifier": objc._C_OUT, "null_accepted": True}}})
            except Exception:
                pass

    @staticmethod
    def ok(result):
        """Calls return (ok, error) when PyObjC knows about the error argument, else just ok."""
        if isinstance(result, tuple):
            return bool(result[0]), result[1]
        return bool(result), None

    def offers_2x(self, w, h):
        try:
            factors = self.Config.supportedScaleFactorsForFrameWidth_frameHeight_(w, h)
            return any(abs(float(f) - 2.0) < 0.01 for f in factors)
        except Exception:
            return False


class _Tile:
    """One running copy of the model for one tile size: a source buffer in, a 2x buffer out."""

    def __init__(self, apple, w, h):
        self.apple, self.Q = apple, apple.Q
        self.w, self.h, self.out_w, self.out_h = w, h, 2 * w, 2 * h
        cfg = apple.Config.alloc().initWithFrameWidth_frameHeight_scaleFactor_(w, h, 2.0)
        if cfg is None:
            raise RuntimeError(f"Apple refused {w}x{h} at 2x")
        self.proc = apple.Processor.alloc().init()
        ok, err = apple.ok(self.proc.startSessionWithConfiguration_error_(cfg, None))
        if not ok:
            raise RuntimeError(f"Apple's upscaling model wouldn't start: {err or 'no details given'}")
        self.src = self._buffer(w, h, cfg.sourcePixelBufferAttributes())
        self.dst = self._buffer(self.out_w, self.out_h, cfg.destinationPixelBufferAttributes())
        self._sync = self.proc.respondsToSelector_(b"processWithParameters:error:")

    def _buffer(self, w, h, attrs):
        Q = self.Q
        attrs = dict(attrs or {})
        attrs.setdefault(Q.kCVPixelBufferIOSurfacePropertiesKey, {})
        attrs[Q.kCVPixelBufferPixelFormatTypeKey] = NV12
        attrs[Q.kCVPixelBufferWidthKey] = w
        attrs[Q.kCVPixelBufferHeightKey] = h
        ret, buf = Q.CVPixelBufferCreate(None, w, h, NV12, attrs, None)
        if ret != 0 or buf is None:
            raise RuntimeError(f"CVPixelBufferCreate {w}x{h} failed ({ret})")
        return buf

    def _planes(self, buf, w, h):
        """numpy views of a pixel buffer's two planes: Y (h rows) and UV (h/2 rows), w bytes per row."""
        Q, views = self.Q, []
        for i, rows in enumerate((h, h // 2)):
            base = Q.CVPixelBufferGetBaseAddressOfPlane(buf, i)
            stride = Q.CVPixelBufferGetBytesPerRowOfPlane(buf, i)
            mem = base.as_buffer(stride * rows)
            views.append(np.ndarray((rows, stride), np.uint8, buffer=mem)[:, :w])
        return views

    def put(self, planes):
        self.Q.CVPixelBufferLockBaseAddress(self.src, 0)
        try:
            for view, plane in zip(self._planes(self.src, self.w, self.h), planes):
                view[:] = plane
        finally:
            self.Q.CVPixelBufferUnlockBaseAddress(self.src, 0)

    def run(self, index):
        with self.apple.pool():
            self._run(index)

    def _run(self, index):
        pts = (index * 1001, 60000, 1, 0)   # CMTime(value, timescale, flags=valid, epoch)
        s = self.apple.Frame.alloc().initWithBuffer_presentationTimeStamp_(self.src, pts)
        d = self.apple.Frame.alloc().initWithBuffer_presentationTimeStamp_(self.dst, pts)
        params = self.apple.Params.alloc().initWithSourceFrame_destinationFrame_(s, d)
        if self._sync:
            ok, err = self.apple.ok(self.proc.processWithParameters_error_(params, None))
            if not ok:
                raise RuntimeError(f"Apple's upscaling model failed on frame {index}: {err or 'no details given'}")
        else:
            done, box = threading.Event(), {}

            def handler(_params, err):
                box["err"] = err
                done.set()
            self.proc.processWithParameters_completionHandler_(params, handler)
            done.wait()
            if box.get("err"):
                raise RuntimeError(f"Apple's upscaling model failed on frame {index}: {box['err']}")

    def open_result(self):
        """The upscaled tile as [Y, UV] views of the model's own memory (no copy). Read them,
        then call close_result() before the tile is used again."""
        self.Q.CVPixelBufferLockBaseAddress(self.dst, 1)
        return self._planes(self.dst, self.out_w, self.out_h)

    def close_result(self):
        self.Q.CVPixelBufferUnlockBaseAddress(self.dst, 1)


class _FakeTile:
    """Test stand-in: doubles every pixel. No Apple hardware needed."""

    def __init__(self, _apple, w, h):
        self.w, self.h = w, h

    def put(self, planes):
        self.planes = [np.array(p) for p in planes]

    def run(self, index):
        pass

    def open_result(self):
        y, uv = self.planes
        pairs = uv.reshape(uv.shape[0], -1, 2).repeat(2, axis=0).repeat(2, axis=1)   # keep U,V pairs together
        return [y.repeat(2, axis=0).repeat(2, axis=1), pairs.reshape(uv.shape[0] * 2, -1)]

    def close_result(self):
        pass


class Upscaler2x:
    """upscaler.process(y, uv, frame_number, out) -> out, filled with (y, uv) at twice the size.
    `out` comes from upscaler.new_output(); worker.py keeps two, so one can be written to the
    encoder while the next picture is being made.

    Each row of tiles is joined left to right into a full-width strip, then the strips are
    joined top to bottom. Every copy of a 4K picture costs time, so the tiles are read
    straight from the model's memory and the top strip is built in place in the result.
    (Running the tiles at the same time on threads was tried and measured: no faster.)"""

    def __init__(self, fmt):
        apple = None if FAKE else _Apple()
        tile_w, tile_h = fmt.tile
        if apple and not apple.offers_2x(tile_w, tile_h):
            raise RuntimeError(f"This Mac does not offer 2x upscaling for {tile_w}x{tile_h} tiles")
        Tile = _FakeTile if FAKE else _Tile
        planes = lambda w, h: [np.zeros((2 * h, 2 * w), np.uint8), np.zeros((h, 2 * w), np.uint8)]   # Y and UV at 2x
        self.tile_w, self.tile_h, self.xs, self.ys = tile_w, tile_h, fmt.tile_xs, fmt.tile_ys
        self.out_w, self.out_h = 2 * fmt.width, 2 * fmt.height
        self.tiles = [[Tile(apple, tile_w, tile_h) for _ in self.xs] for _ in self.ys]        # rows of tiles
        self.lower_strips = [planes(fmt.width, tile_h) for _ in self.ys[1:]]                  # the top strip is built in the result

    def new_output(self):
        """Empty Y and UV planes for one upscaled picture."""
        return [np.zeros((self.out_h, self.out_w), np.uint8), np.zeros((self.out_h // 2, self.out_w), np.uint8)]

    def process(self, y, uv, index, out):
        w, h = self.tile_w, self.tile_h
        strips = [[out[0][:2 * h], out[1][:h]]] + self.lower_strips                          # top strip: the top rows of out
        for row, top in enumerate(self.ys):
            for col, left in enumerate(self.xs):
                tile = self.tiles[row][col]
                tile.put([y[top:top + h, left:left + w], uv[top // 2:(top + h) // 2, left:left + w]])
                tile.run(index)
                filled = 2 * (self.xs[col - 1] + w) if col else 0            # how far the strip is filled so far
                for plane, upscaled in enumerate(tile.open_result()):        # brightness, then color
                    add_piece(strips[row][plane], upscaled, 2 * left, filled, across=True, pairs=plane == 1)
                tile.close_result()
        for row in range(1, len(self.ys)):                                   # the top strip is already in place
            for plane, lines in ((0, 2), (1, 1)):                            # result rows per source row; color has half
                add_piece(out[plane], strips[row][plane], lines * self.ys[row],
                          lines * (self.ys[row - 1] + h), across=False)
        return out


def add_piece(out, piece, start, filled, across, pairs=False):
    """Put `piece` into `out` beginning at column `start` (across=True) or at row `start` (across=False).
    `out` is already filled up to `filled`. Where the piece overlaps what is there, the picture
    fades from the old to the new, so no seam shows.
    pairs: the columns come in U,V pairs (NV12's color plane); both of a pair get the same weight."""
    cut = (lambda a, b: (slice(None), slice(a, b))) if across else (lambda a, b: (slice(a, b),))
    size = piece.shape[1] if across else piece.shape[0]
    overlap = filled - start
    if overlap > 0:
        fade = np.linspace(256, 0, overlap // 2 if pairs else overlap).round().astype(np.uint16)
        if pairs:
            fade = np.repeat(fade, 2)
        if not across:
            fade = fade[:, None]                             # one weight per row instead of per column
        old, new = out[cut(start, filled)].astype(np.uint16), piece[cut(0, overlap)].astype(np.uint16)
        out[cut(start, filled)] = ((old * fade + new * (256 - fade) + 128) >> 8).astype(np.uint8)
    out[cut(filled, start + size)] = piece[cut(max(overlap, 0), size)]
