"""Download the models into models/ (skips files that are already there):

    python tools/download_models.py

InsightFace buffalo_l (face detector SCRFD + ArcFace; weights: non-commercial use only) and SigLIP2 (scenes, text search).
"""
import io
import sys
import time
import urllib.request
import zipfile
from pathlib import Path

MODELS = Path(__file__).resolve().parent.parent / "models"
BUFFALO = "https://github.com/deepinsight/insightface/releases/download/v0.7/buffalo_l.zip"
SIGLIP = "https://huggingface.co/immich-app/ViT-B-16-SigLIP2__webli/resolve/main/"
SIGLIP_FILES = ["config.json", "visual/model.onnx", "visual/preprocess_cfg.json", "textual/model.onnx",
                "textual/tokenizer.json", "textual/tokenizer_config.json", "textual/special_tokens_map.json"]


def fetch(url, tries=10):
    """Download into memory; a dropped or stalled connection resumes where it stopped (slow networks)."""
    print(f"  {url}", flush=True)
    buf, total, shown = bytearray(), 0, 0
    for attempt in range(tries):
        try:
            req = urllib.request.Request(url, headers={"Range": f"bytes={len(buf)}-"} if buf else {})
            with urllib.request.urlopen(req, timeout=60) as r:
                if buf and r.status != 206:
                    buf.clear()  # the server ignored Range: start over
                total = total or len(buf) + int(r.headers.get("Content-Length") or 0)
                while chunk := r.read(1 << 20):
                    buf += chunk
                    if total and len(buf) * 10 // total > shown:  # every 10%: a slow download must not look hung
                        shown = len(buf) * 10 // total
                        print(f"  {shown * 10}% of {total >> 20} MB", flush=True)
            return bytes(buf)
        except OSError as e:  # URLError, timeouts, resets
            if attempt == tries - 1:
                raise
            print(f"  connection problem ({e}), retrying in 10 s from {len(buf) >> 20} MB...", flush=True)
            time.sleep(10)


def save(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".part")
    tmp.write_bytes(data)
    tmp.replace(path)  # never leave a half-written model behind


def main():
    face = [MODELS / "det_10g.onnx", MODELS / "w600k_r50.onnx"]
    if not all(p.exists() for p in face):
        print("Faces: InsightFace buffalo_l (~280 MB)")
        with zipfile.ZipFile(io.BytesIO(fetch(BUFFALO))) as z:
            for p in face:
                name = next(n for n in z.namelist() if n.endswith("/" + p.name) or n == p.name)
                save(p, z.read(name))
    for f in SIGLIP_FILES:
        p = MODELS / "siglip2" / f
        if not p.exists():
            print(f"Scenes: SigLIP2 {f}")
            save(p, fetch(SIGLIP + f))
    missing = [p for p in face + [MODELS / "siglip2" / f for f in SIGLIP_FILES] if not p.stat().st_size]
    if missing:
        sys.exit(f"empty model files: {missing}")
    print("Models are ready:", MODELS)


if __name__ == "__main__":
    main()
