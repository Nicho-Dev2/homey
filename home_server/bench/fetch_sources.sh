#!/bin/bash
# Downloads clean "ground truth" test clips into bench/sources/ and makes
# previews for review.  Run on the Mac:   cd ~/homey/home_server/bench && bash fetch_sources.sh
# Safe to re-run: finished downloads are skipped, partial ones resume.
# Nothing is deleted. Needs ffmpeg and ~45 GB free (Inter4K alone is ~15 GB zipped + ~15 GB unzipped).
# Uses aria2c (8 connections) when installed:  brew install aria2   -- otherwise curl.
set -u
cd "$(dirname "$0")"
mkdir -p sources/downloads sources/preview
FAILED=()

get() {  # get <url>  -> sources/downloads/<name>
  local url="$1" name out
  name="$(basename "$1")"; out="sources/downloads/$name"
  if [ -f "$out.done" ]; then echo "have   $name"; return 0; fi
  echo "fetch  $url"
  if command -v aria2c >/dev/null; then
    # -c continues a partial file (also one that curl started)
    aria2c -c -x 8 -s 8 -k 4M --file-allocation=none --auto-file-renaming=false \
           --console-log-level=warn --summary-interval=0 -d sources/downloads -o "$name" "$url"
  else
    curl -fL --retry 3 -C - -o "$out" "$url"
  fi
  if [ $? -eq 0 ] && [ -s "$out" ]; then touch "$out.done"; return 0; fi
  echo "  !! download failed: $url"; return 1
}

get_first() {  # try several addresses for the same clip; sets GOT to the file that worked
  GOT=""
  for url in "$@"; do
    if get "$url"; then GOT="sources/downloads/$(basename "$url")"; return 0; fi
  done
  FAILED+=("$1"); return 1
}

master() {  # master <name> <ffmpeg input args...>   -> sources/<name>.mkv (near-lossless H.264)
  local name="$1"; shift
  [ -f "sources/$name.mkv" ] && { echo "have   sources/$name.mkv"; return 0; }
  echo "encode sources/$name.mkv"
  "$@" -an -c:v libx264 -preset fast -crf 10 -pix_fmt yuv420p \
       -color_primaries bt709 -color_trc bt709 -colorspace bt709 \
       -y "sources/$name.tmp.mkv" && mv "sources/$name.tmp.mkv" "sources/$name.mkv" \
       || { echo "  !! encode failed: $name"; FAILED+=("$name"); }
}

preview() {  # preview <name>  -> small 1080p file + contact sheet
  local name="$1"; [ -f "sources/$name.mkv" ] || return 0
  [ -f "sources/preview/$name.mp4" ] || ffmpeg -v error -i "sources/$name.mkv" \
      -vf scale=1920:-2 -c:v libx264 -crf 18 -pix_fmt yuv420p -y "sources/preview/$name.mp4"
  [ -f "sources/preview/$name.png" ] || ffmpeg -v error -i "sources/$name.mkv" \
      -vf "fps=1,scale=960:-2,tile=3x2" -frames:v 1 -y "sources/preview/$name.png"
}

# --- UVG dataset (Tampere University, CC BY-NC): 4K 120 fps horse racing, raw YUV in .7z ---
# Every other frame is kept -> 4K 60 fps, 5 seconds each (300 frames). Decoded straight from
# the archive. "-frames:v 300" stops at the last picture, before the small text file that
# is also in the archive (that file caused the harmless "Packet too small" message).
uvg() {  # uvg <short-name> <file-name candidates...>
  local lc="$1"; shift
  if [ -f "sources/uvg_${lc}_2160p60.mkv" ]; then echo "have   sources/uvg_${lc}_2160p60.mkv"; return 0; fi   # already converted
  local urls=(); for f in "$@"; do urls+=("https://ultravideo.fi/video/${f}_3840x2160_120fps_420_8bit_YUV_RAW.7z"); done
  if get_first "${urls[@]}"; then
    master "uvg_${lc}_2160p60" bash -c 'tar -xOf "$0" | ffmpeg -v error -stats -f rawvideo \
      -pix_fmt yuv420p -s 3840x2160 -r 120 -i - -vf "select=not(mod(n\,2)),setpts=N/60/TB" -r 60 -frames:v 300 "$@"' \
      "$GOT"
    preview "uvg_${lc}_2160p60"
  fi
}
uvg jockey Jockey
uvg readysetgo ReadySetGo ReadySteadyGo

# --- Xiph.org test media: 1080p 29.97 fps American football (.y4m, uncompressed) ---
for seq in touchdown_pass rush_field_cuts; do
  if [ -f "sources/xiph_${seq}_1080p30.mkv" ]; then echo "have   sources/xiph_${seq}_1080p30.mkv"; continue; fi   # already converted
  if get_first "https://media.xiph.org/video/derf/y4m/${seq}_1080p.y4m"; then
    master "xiph_${seq}_1080p30" ffmpeg -v error -stats -i "$GOT"
    preview "xiph_${seq}_1080p30"
  fi
done

# --- Inter4K (research dataset): 1,000 five-second clips, 4K 60 fps, about 15 GB zipped ---
# Used for training only. The short link forwards to Google Drive, which normally wants a
# browser click: this download then gets a small web page instead of the zip. In that case
# open the link in a browser, save the file as sources/downloads/Inter4K.zip, and re-run.
INTER_ZIP="sources/downloads/Inter4K.zip"
inter_have() { find sources/inter4k -name '*.mp4' ! -name '._*' ! -path '*__MACOSX*' 2>/dev/null | wc -l | tr -d ' '; }
if [ "$(inter_have)" -ge 1000 ]; then
  echo "have   all 1000 Inter4K clips"
elif [ -f "$INTER_ZIP" ] && unzip -tq "$INTER_ZIP" >/dev/null 2>&1; then
  touch "$INTER_ZIP.done"                 # a zip saved by hand from the browser
elif [ ! -f "$INTER_ZIP.done" ]; then
  [ -f "$INTER_ZIP" ] && [ "$(wc -c < "$INTER_ZIP")" -lt 1000000 ] && rm -f "$INTER_ZIP"   # a web page from an earlier try
  echo "fetch  Inter4K (about 15 GB)"
  if command -v aria2c >/dev/null; then
    aria2c -c -x 8 -s 8 -k 4M --file-allocation=none --auto-file-renaming=false \
           --console-log-level=warn --summary-interval=0 -d sources/downloads -o Inter4K.zip \
           "https://tinyurl.com/inter4KUHD"
  else
    curl -fL --retry 3 -C - -o "$INTER_ZIP" "https://tinyurl.com/inter4KUHD"
  fi
  if [ -f "$INTER_ZIP" ] && unzip -tq "$INTER_ZIP" >/dev/null 2>&1; then
    touch "$INTER_ZIP.done"
  else
    echo "  !! Inter4K did not arrive as a zip file. Open https://tinyurl.com/inter4KUHD in a browser,"
    echo "     save it as bench/$INTER_ZIP, then run this script again."
    rm -f "$INTER_ZIP"
    FAILED+=("Inter4K")
  fi
fi
if [ -f "$INTER_ZIP.done" ]; then
  mkdir -p sources/inter4k
  inter_count() { find sources/inter4k -name '*.mp4' ! -name '._*' ! -path '*__MACOSX*' | wc -l | tr -d ' '; }
  if [ "$(inter_count)" -lt 1000 ] && [ -f "$INTER_ZIP" ]; then      # also finishes an interrupted unzip
    echo "unzip  Inter4K -> sources/inter4k/ (files already there are kept)"
    unzip -nq "$INTER_ZIP" -d sources/inter4k -x '__MACOSX/*' || FAILED+=("Inter4K unzip")
  fi
  echo "have   $(inter_count) of 1000 Inter4K clips"
fi

echo; echo "== Result =="
ls -lh sources/*.mkv sources/preview 2>/dev/null
du -sh sources/downloads 2>/dev/null
if [ ${#FAILED[@]} -gt 0 ]; then echo; echo "FAILED:"; printf '  %s\n' "${FAILED[@]}"; fi
echo; echo "Review the files in sources/preview/ (open sources/preview)."
