"""Things the parts of the enhancer share: the broadcast formats it handles and small helpers."""
import os
from dataclasses import dataclass

FAKE = os.environ.get("ENHANCE_FAKE") == "1"   # stand-ins for Apple's models, for testing on any computer
OUT_FPS = "60000/1001"                         # every enhanced stream is 59.94 pictures a second
BITRATE = "40M"     # video bitrate of every enhanced stream. Keep it at 40M or less: above that the encoder
                    # marks a 4K stream HEVC level 5.2, which the Fire TV will not play (measured).


@dataclass(frozen=True)
class Format:
    """One kind of broadcast and the settings used to enhance it. Sizes are in pixels."""
    name: str
    width: int
    height: int
    interlaced: bool    # True: each frame holds two half-pictures ("fields") taken 1/60 s apart
    model: str          # the cleanup network for this picture size, in models/
    sharpener: str      # the filter that does the light sharpening (see SHARPNESS below)
    tile: tuple         # (width, height) of one piece handed to the 2x upscaler
    tile_xs: tuple      # where the pieces start, left to right ...
    tile_ys: tuple      # ... and top to bottom. Neighbors overlap so the joins can be faded.

    @property
    def frame_bytes(self):
        """Bytes in one frame (NV12: a brightness plane plus a half-height color plane)."""
        return self.width * self.height * 3 // 2

    @property
    def frames_a_second(self):
        """Frames a second as broadcast. An interlaced frame carries two fields, so 29.97."""
        return "30000/1001" if self.interlaced else "60000/1001"

    @property
    def frame_seconds(self):
        return 1001 / 30000 if self.interlaced else 1001 / 60000


FORMATS = {f.name: f for f in (
    #       name     size        interlaced  cleanup network          sharpener  tile        tiles start at
    Format("720p",  1280, 720,   False,      "look1.mlpackage",       "cas",     (704, 720), (0, 576),       (0,)),       # -> 1440p
    Format("1080i", 1920, 1080,  True,       "look1_1080i.mlpackage", "unsharp", (672, 560), (0, 624, 1248), (0, 520)),   # -> 4K
)}

# How strong each sharpening filter is set by default. The two numbers give the same look: measured
# on 1080i test clips, unsharp at 0.3 lifts edges as much as cas at 0.4 and scores the same (VMAF
# within 0.1), for an eighth of the processor time. 1080i uses it because processor heat is what
# makes a fanless Mac fall behind there.
SHARPNESS = {"cas": 0.4, "unsharp": 0.3}

# The 1080i tiles are as small as keeps a fade of 48 (across) and 40 (down) pixels where neighbors
# overlap: upscaling is about 93% of the 1080i work, and 672x560 tiles are 7% fewer pixels than the
# 704x576 used before (measured need: a hot fanless Mac fell 2-3% short of live speed).


def read_exact(pipe, n):
    """Read exactly n bytes from a pipe, or None once the pipe has ended."""
    buf = bytearray(n)
    view, got = memoryview(buf), 0
    while got < n:
        k = pipe.readinto(view[got:])
        if not k:
            return None
        got += k
    return buf


def say(message):
    """A note for the upscaler's log. '[info]' lines are routine; anything else is shown as a problem."""
    os.write(2, (message.rstrip() + "\n").encode())
