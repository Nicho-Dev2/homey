"""The cleanup networks: small stacks of convolutions that take damaged 720p
frames and predict the correction that brings the picture back toward the clean one.

Both work on the picture at half size: the brightness plane (Y) is folded 2x2 into
4 channels and joined with the two color planes (U, V), 6 channels per frame at
640x360 instead of 1280x720. That is what keeps them fast enough for live TV.

  CleanupNet   (v1) looks at one frame.
  CleanupNetT  (v2) looks at the frame plus its neighbors (before and after), and does
               most of its work at quarter size (320x180), which is cheap and lets it
               see far enough sideways to find the same detail in the other frames.
"""
import torch
import torch.nn as nn
import torch.nn.functional as F

# v2 sizes, smallest/fastest first. speed_cleanup.py measures which one fits live TV.
SIZES = {
    "small":  {"channels": 24, "mid": 48, "mid_blocks": 3},
    "medium": {"channels": 32, "mid": 64, "mid_blocks": 4},
    "large":  {"channels": 48, "mid": 96, "mid_blocks": 5},
}


class CleanupNet(nn.Module):
    def __init__(self, channels=32, blocks=4):
        super().__init__()
        self.head = nn.Conv2d(6, channels, 3, padding=1)
        self.body = nn.ModuleList(nn.Conv2d(channels, channels, 3, padding=1) for _ in range(blocks))
        self.tail = nn.Conv2d(channels, 6, 3, padding=1)
        nn.init.zeros_(self.tail.weight)      # start as "change nothing"
        nn.init.zeros_(self.tail.bias)

    def forward(self, x):                     # x: (batch, 6, H/2, W/2), values 0..1
        f = F.relu(self.head(x))
        for conv in self.body:
            f = F.relu(conv(f))
        return x + self.tail(f)               # picture + correction


class CleanupNetT(nn.Module):
    def __init__(self, frames=3, channels=32, mid=64, mid_blocks=4):
        super().__init__()
        self.frames = frames
        self.head = nn.Conv2d(6 * frames, channels, 3, padding=1)
        self.enc = nn.Conv2d(channels, channels, 3, padding=1)
        self.down = nn.Conv2d(channels, mid, 3, stride=2, padding=1)       # to quarter size
        self.mid = nn.ModuleList(nn.Conv2d(mid, mid, 3, padding=1) for _ in range(mid_blocks))
        self.up = nn.Conv2d(mid, channels, 1)
        self.dec = nn.Conv2d(channels, channels, 3, padding=1)
        self.tail = nn.Conv2d(channels, 6, 3, padding=1)
        nn.init.zeros_(self.tail.weight)
        nn.init.zeros_(self.tail.bias)

    def forward(self, x):                     # x: (batch, 6*frames, H/2, W/2), values 0..1
        c = 6 * (self.frames // 2)
        f = F.relu(self.head(x))
        f = F.relu(self.enc(f))
        m = F.relu(self.down(f))
        for conv in self.mid:
            m = F.relu(conv(m))
        u = F.interpolate(self.up(m), scale_factor=2, mode="nearest")      # back to half size
        f = F.relu(self.dec(f + u))
        return x[:, c:c + 6] + self.tail(f)   # the middle frame + correction


def build(arch, kwargs):
    return CleanupNetT(**kwargs) if arch == "t" else CleanupNet(**kwargs)


def pack(y, uv):
    """One frame. y: (B,1,H,W), uv: (B,2,H/2,W/2), floats 0..1  ->  (B,6,H/2,W/2)"""
    return torch.cat([F.pixel_unshuffle(y, 2), uv], dim=1)


def pack_t(y, uv):
    """Several frames. y: (B,T,H,W), uv: (B,T,2,H/2,W/2)  ->  (B,6*T,H/2,W/2), frame by frame."""
    b, t, h, w = y.shape
    yp = F.pixel_unshuffle(y.reshape(b * t, 1, h, w), 2).reshape(b, t, 4, h // 2, w // 2)
    return torch.cat([yp, uv], dim=2).reshape(b, t * 6, h // 2, w // 2)


def unpack(x):
    """(B,6,H/2,W/2) -> y (B,1,H,W), uv (B,2,H/2,W/2)"""
    return F.pixel_shuffle(x[:, :4], 2), x[:, 4:]


def pick_device():
    if torch.backends.mps.is_available():
        return torch.device("mps")            # the Mac's GPU
    return torch.device("cuda" if torch.cuda.is_available() else "cpu")
