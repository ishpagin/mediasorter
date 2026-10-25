"""Mediasorter logo: a 2 x 2 contact sheet, the best shot circled by a yellow grease pencil
(picking frames on the light table) + the wordmark in Onest SemiBold as outlines (no font needed to show it).

    python build_logo.py <Onest.ttf> <out dir>
"""
import sys
from pathlib import Path

from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

FONT, OUT = sys.argv[1], Path(sys.argv[2])
OUT.mkdir(parents=True, exist_ok=True)
EASE = "cubic-bezier(0.2, 0.8, 0.2, 1)"

# 48 x 48 mark: a 2 x 2 contact sheet of four different shots; the grease pencil circles the best one
TILES = [(6, 6, "#A9BED1"), (26, 6, "#E8C99B"), (6, 26, "#8FA59B"), (26, 26, "#B9AFC9")]  # x, y, photo tone
PICK = 1  # top right
LOOP = ("M44.2 9.0 C47.2 15.6 43.4 23.6 35.6 25.8 C28.0 28.0 21.2 23.4 21.4 16.2 "
        "C21.6 8.6 28.6 3.6 36.4 4.4 C41.8 5.0 45.6 8.6 46.4 13.8")  # tilted, the end crosses the start: by hand


def mark(p, animated, mount_edge):
    """The mark's SVG elements (48 x 48), ids/classes prefixed with p so several can live in one page."""
    style = ""
    if animated:  # tiles lay out one by one (like the app's grid), the picked one stays bright, then the loop
        style = f"""<style>
  .{p}t {{ animation: {p}in 380ms {EASE} both; }}
  .{p}t1 {{ animation-delay: 90ms; }} .{p}t2 {{ animation-delay: 180ms; }} .{p}t3 {{ animation-delay: 270ms; }}
  .{p}dim {{ animation: {p}dim 500ms ease-out 900ms both; }}
  .{p}loop {{ stroke-dasharray: 100; animation: {p}draw 560ms {EASE} 700ms both; }}
  @keyframes {p}in {{ from {{ opacity: 0; transform: translateY(5px); }} to {{ opacity: 1; transform: none; }} }}
  @keyframes {p}dim {{ from {{ opacity: 1; }} to {{ opacity: .55; }} }}
  @keyframes {p}draw {{ from {{ stroke-dashoffset: 100; }} to {{ stroke-dashoffset: 0; }} }}
  @media (prefers-reduced-motion: reduce) {{ .{p}t, .{p}dim, .{p}loop {{ animation: none; }} }}
</style>"""
    edge = f' stroke="{mount_edge}" stroke-width="0.75"' if mount_edge else ""
    tiles = []
    for i, (x, y, tone) in enumerate(TILES):
        dim = "" if i == PICK else f' class="{p}dim"' if animated else ' opacity=".55"'
        tiles.append(f'<g class="{p}t {p}t{i}"><g{dim}>'
                     f'<rect x="{x}" y="{y}" width="16" height="16" rx="2.5" fill="#FBFBF8"{edge}/>'
                     f'<rect x="{x + 2}" y="{y + 2}" width="12" height="12" rx="1" fill="{tone}"/></g></g>')
    return f"""{style}
{chr(10).join(tiles)}
<path class="{p}loop" pathLength="100" d="{LOOP}" fill="none" stroke="#F2B705" stroke-width="2.8"
      stroke-linecap="round" stroke-linejoin="round"/>"""


def wordmark(text, size, tracking=-0.015):
    """Outlines of text in Onest SemiBold: (svg path d, width), baseline at y = size * 0.8."""
    f = TTFont(FONT)
    f = instantiateVariableFont(f, {"wght": 600})
    gs, cmap, hmtx = f.getGlyphSet(), f.getBestCmap(), f["hmtx"]
    upm = f["head"].unitsPerEm
    k = size / upm
    pen = SVGPathPen(gs)
    x = 0.0
    for ch in text:
        g = cmap[ord(ch)]
        gs[g].draw(TransformPen(pen, (k, 0, 0, -k, x, size * 0.8)))
        x += hmtx[g][0] * k + tracking * size
    return pen.getCommands(), x - tracking * size


def svg(w, h, body, label):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w:.1f} {h}" width="{w:.1f}" height="{h}" '
            f'role="img" aria-label="{label}">\n<title>{label}</title>{body}\n</svg>\n')


def lockup(animated, text_color, mount_edge, p):
    d, tw = wordmark("Mediasorter", 26)
    fade = ""
    if animated:
        fade = (f"<style>.{p}word {{ animation: {p}up 520ms {EASE} 1000ms both; }} "
                f"@keyframes {p}up {{ from {{ opacity: 0; transform: translateY(4px); }} to {{ opacity: 1; transform: none; }} }} "
                f"@media (prefers-reduced-motion: reduce) {{ .{p}word {{ animation: none; }} }}</style>")
    body = (mark(p, animated, mount_edge) + fade +
            f'\n<g transform="translate(58 6)"><path class="{p}word" fill="{text_color}" d="{d}"/></g>')
    return svg(58 + tw + 2, 48, body, "Mediasorter")


(OUT / "mark.svg").write_text(svg(48, 48, mark("m", False, None), "Mediasorter"), encoding="utf-8")
(OUT / "mark-animated.svg").write_text(svg(48, 48, mark("a", True, None), "Mediasorter"), encoding="utf-8")
(OUT / "logo.svg").write_text(lockup(False, "#E6E8EB", None, "l"), encoding="utf-8")            # sidebar (graphite)
(OUT / "logo-readme-dark.svg").write_text(lockup(True, "#E6E8EB", None, "d"), encoding="utf-8")  # GitHub dark theme
(OUT / "logo-readme-light.svg").write_text(lockup(True, "#1D2024", "#CDD3DA", "w"), encoding="utf-8")
print("ok:", sorted(p.name for p in OUT.iterdir()))
