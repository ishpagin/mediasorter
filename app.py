"""Web UI over faces.db:  streamlit run app.py  [-- --db path\\to\\faces.db]"""
import argparse
import hashlib
import os
import sys
from datetime import datetime

import cv2
import numpy as np
import streamlit as st

import media_server
import photosorter as ps
import scenes
import stacks
from grid import photo_grid

PAGE = 60  # items loaded per portion

_ap = argparse.ArgumentParser()
_ap.add_argument("--db", default=ps.DEFAULT_DB)
DB = _ap.parse_known_args(sys.argv[1:])[0].db

st.set_page_config(page_title="Mediasorter", layout="wide")

st.html("""<style>
/* light table: tighter headings; the sidebar is the graphite frame of the table */
h1 { letter-spacing: -0.02em; }
h2, h3 { letter-spacing: -0.01em; }
h1, h2, h3 { text-wrap: balance; }
[data-testid="stSidebar"] [data-testid="stSidebarNav"] a[aria-current="page"] { box-shadow: inset 3px 0 0 #F2B705; }
/* cards (people, collections, similar pairs) are slide mounts too, like the photos in the grid */
[class*="st-key-mount_"] {
    background: #FBFBF8; border-color: transparent !important; border-radius: 4px !important;
    box-shadow: 0 0 0 1px rgba(29, 32, 36, .07), 0 1px 2px rgba(36, 48, 66, .10), 0 6px 14px -8px rgba(36, 48, 66, .28);
}
[class*="st-key-mount_"] img { border-radius: 2px; }
</style>""")
con = stacks.open_db(DB)
state = st.session_state


# ---------------------------------------------------------------- URL state + scroll memory

def url_init(key, name, default, parse=str):
    """Initial value of widget `key` from URL parameter `name` (when the widget is (re)created)."""
    if key not in state:
        raw = st.query_params.get(name)
        try:
            state[key] = parse(raw) if raw not in (None, "") else default
        except ValueError:
            state[key] = default


def url_set(name, value, default=None):
    """Mirror a filter in the URL (defaults are left out to keep links short)."""
    if isinstance(value, (list, tuple)):
        value = ",".join(map(str, value))
    if value in (default, "", None, False):
        if name in st.query_params:
            del st.query_params[name]
    elif st.query_params.get(name) != str(value):
        st.query_params[name] = str(value)


def ids_param(raw):
    return [int(x) for x in raw.split(",") if x.strip().isdigit()]


def url_page(page):
    """Each page remembers its URL parameters: coming back through the menu restores the filters."""
    saved = state.setdefault("_qp", {})
    if not st.query_params.to_dict() and saved.get(page):
        st.query_params.from_dict(saved[page])


def url_done(page):
    """End of a page: remember its URL and restore/keep the scroll position for exactly this view."""
    params = st.query_params.to_dict()
    state.setdefault("_qp", {})[page] = params
    _scroll_memory(key="scrollmem", data={"key": page + "?" + "&".join(f"{k}={v}" for k, v in sorted(params.items()))})


# remembers the scroll position per view (page + filters) in sessionStorage (survives F5) and restores it,
# letting infinite scroll load more items until the position is reachable; user input cancels restoring
_SCROLL_MEM_JS = """
export default function(component) {
    const { data } = component;
    const g = (window.__psScrollMem = window.__psScrollMem || {});
    const sc = () => {
        const m = document.querySelector('[data-testid="stMain"]');
        if (m && /(auto|scroll)/.test(getComputedStyle(m).overflowY)) return m;
        return document.scrollingElement;
    };
    if (!g.installed) {
        g.installed = true;
        const user = (e) => {
            // a click (link, button, card) may navigate: the page then shrinks and the browser jumps to the top;
            // that jump must not be saved as the position, so a click closes the "user is scrolling" window
            if (e.type === 'mousedown' && e.target !== sc()) { g.userAt = 0; return; }
            g.userAt = Date.now();
            g.restoring = false;
        };
        for (const t of ['wheel', 'touchstart', 'keydown', 'mousedown'])
            window.addEventListener(t, user, { passive: true, capture: true });
        window.addEventListener('scroll', (e) => {
            const s = sc();
            if (e.target !== s && e.target !== document) return;
            if (!g.key || g.restoring || Date.now() - (g.userAt || 0) > 1500) return;  // user scrolling only
            if (s.scrollHeight < (g.height || 0) * 0.8) return;  // content was just replaced, not a real scroll
            g.height = s.scrollHeight;
            try { sessionStorage.setItem('ps-scroll:' + g.key, String(Math.round(s.scrollTop))); } catch (_) {}
        }, { passive: true, capture: true });
    }
    if (g.key === data.key) return;
    g.key = data.key;
    g.height = 0;
    clearInterval(g.timer);
    let target = 0;
    try { target = Number(sessionStorage.getItem('ps-scroll:' + data.key) || 0); } catch (_) {}
    const s = sc();
    if (!target) { s.scrollTop = 0; g.restoring = false; return; }
    g.restoring = true;
    const t0 = Date.now();
    g.timer = setInterval(() => {
        if (!g.restoring || Date.now() - t0 > 60000) { clearInterval(g.timer); g.restoring = false; return; }
        s.scrollTop = target;  // near the end this triggers infinite scroll, so the list grows until it fits
        if (Math.abs(s.scrollTop - target) < 2 && Date.now() - t0 > 700) { clearInterval(g.timer); g.restoring = false; }
    }, 100);
}
"""
_scroll_memory = st.components.v2.component("scroll_memory", js=_SCROLL_MEM_JS)


@st.cache_resource(show_spinner=False)
def media_server_for(db):
    """One local originals server per DB for the whole Streamlit process: (base_url, token)."""
    return media_server.start(db)


def preview(fid):
    """Stored preview of a file (first one for videos) — no reads from the originals."""
    row = con.execute("SELECT jpeg FROM previews WHERE file_id = ? ORDER BY t_sec LIMIT 1", (fid,)).fetchone()
    return row[0] if row else None


@st.cache_data(max_entries=5000, show_spinner=False)
def square(jpeg, size=192):
    """Center-crop a JPEG to a square so all cards have the same size."""
    img = cv2.imdecode(np.frombuffer(jpeg, np.uint8), cv2.IMREAD_COLOR)
    h, w = img.shape[:2]
    s = min(h, w)
    img = cv2.resize(img[(h - s) // 2:(h + s) // 2, (w - s) // 2:(w + s) // 2], (size, size), interpolation=cv2.INTER_AREA)
    return cv2.imencode(".jpg", img, [cv2.IMWRITE_JPEG_QUALITY, 85])[1].tobytes()


def label(pid, name):
    return name or f"person_{pid}"


def file_caption(path, kind, taken_at, place, t=None):
    cap = os.path.basename(path)
    if kind == "video":
        cap = f"🎬 {cap}" + (f" · {t:.0f}s" if t is not None else "")
    info = " · ".join(x for x in ((taken_at or "")[:10], place) if x)
    return cap + (f"\n{info}" if info else "")


# ---------------------------------------------------------------- infinite scroll (people cards)

# an invisible marker after the cards; when it comes into view, JS asks Python for more
_SCROLL_JS = """
export default function(component) {
    const { data, setTriggerValue, parentElement } = component;
    let el = parentElement.querySelector('.sentinel');
    if (!el) {
        el = document.createElement('div');
        el.className = 'sentinel';
        el.style.height = '1px';
        parentElement.appendChild(el);
    }
    if (el._obs) el._obs.disconnect();
    let fired = false;
    el._obs = new IntersectionObserver((entries) => {
        if (!fired && entries.some((e) => e.isIntersecting)) {
            fired = true;
            setTriggerValue('more', data.limit);
        }
    }, { rootMargin: '1200px' });  // start loading well before the end is reached
    el._obs.observe(el);
}
"""
_sentinel = st.components.v2.component("infinite_scroll", js=_SCROLL_JS)


def _list_id(key):
    # list keys contain user input (search text...); component ids must not contain "__" etc.
    return hashlib.md5(key.encode()).hexdigest()[:16]


def shown(key):
    """How many items of list `key` to render now (grows as the user scrolls)."""
    return state.setdefault(f"lim_{_list_id(key)}", PAGE)


def _bump(lid, limit):
    return lambda: state.__setitem__(f"lim_{lid}", limit + PAGE)


def load_more(key, total):
    """Put after the cards: shows progress and loads the next portion when scrolled into view."""
    limit, lid = shown(key), _list_id(key)
    st.caption(f"Показано {min(limit, total)} из {total}")
    if limit < total:
        _sentinel(key=f"inf{lid}", data={"limit": limit}, on_more_change=_bump(lid, limit))


# ---------------------------------------------------------------- file grid + deletion

@st.dialog("Удалить файлы?", width="large")
def confirm_delete(paths):
    """Bulk deletions (stacks / copies: keep the best) still ask first; Ctrl+Z brings them back too."""
    st.write(f"Будет удалено **{len(paths)}** файл(ов). Ctrl+Z вернёт их.")
    st.code("\n".join(paths[:15]) + (f"\n... и ещё {len(paths) - 15}" if len(paths) > 15 else ""))
    if st.button("Удалить", type="primary"):
        delete_files(paths)


def delete_files(paths):
    """Delete right away, no dialog: Ctrl+Z (several times) brings files back."""
    try:
        delete_now(paths)
    except PermissionError as e:
        state.flash = f"⛔ {e}"
    st.rerun()


def delete_now(paths, one_by_one=False):
    """Delete without asking; every batch can be undone with Ctrl+Z.
    one_by_one: each file is its own undo step (fast Ctrl+D presses may arrive here together)."""
    done, failed, log_ids = ps.trash_files(con, paths)
    stack = state.setdefault("undo", [])
    if one_by_one:
        stack.extend({"kind": "delete", "ids": [lid]} for lid in log_ids)
    elif log_ids:
        stack.append({"kind": "delete", "ids": log_ids})
    name = os.path.basename(paths[0]) if len(paths) == 1 else f"{done} файл(ов)"
    state.flash = f"Удалено: {name} · Ctrl+Z — вернуть" + (f" · ошибок: {len(failed)}" if failed else "")


WHO_MAX = 60  # faces in one "Кто на фото" dialog
NOBODY = -1   # selectbox value: not anybody known (a stranger / not a face)


@st.dialog("Кто на фото", width="large")
def who_dialog(file_ids, src=None):
    """Set by hand who each face on the selected files is; several people on one photo — each face separately.
    src: the person whose page this is: their faces are the ones to fix, they come first."""
    units = ps.file_faces(con, file_ids)
    if src is not None:
        units.sort(key=lambda u: u["person_id"] != src)  # stable: file order kept within each part
    if not units:
        st.info("На выбранных файлах не найдено ни одного лица.")
        return
    nonce = state.get("who_nonce", 0)
    shown = units[:WHO_MAX]

    named = con.execute("SELECT id, name FROM persons WHERE name IS NOT NULL ORDER BY lower(name)").fetchall()
    present = {u["person_id"] for u in units} - {None}
    unnamed = con.execute(f"SELECT id, name FROM persons WHERE name IS NULL AND id IN "
                          f"({','.join('?' * len(present))})", list(present)).fetchall() if present else []
    names = {NOBODY: "— никто из знакомых —", **{pid: label(pid, n) for pid, n in named + unnamed}}
    opts = list(names)

    c1, c2 = st.columns([3, 1], vertical_alignment="bottom")
    new_name = c1.text_input("Нет в списке? Новый человек", key=f"who_new_{nonce}", placeholder="Имя")
    if c2.button("➕ Добавить", disabled=not new_name.strip(), width="stretch"):
        state[f"who_bulk_{nonce}"] = ps.create_person(con, new_name.strip())
        set_all(nonce, shown, src)
        st.rerun(scope="fragment")

    b1, b2 = st.columns([2, 3], vertical_alignment="bottom")
    if src is not None:
        b1.radio("Кому", ["src", "all"], key=f"who_scope_{nonce}", on_change=lambda: set_all(nonce, shown, src),
                 format_func={"src": f"только лицам «{names.get(src, '')}»",
                              "all": "всем лицам на выбранных фото"}.get)
    b2.selectbox("Быстро: поставить всем сразу →", [None] + opts, key=f"who_bulk_{nonce}",
                 format_func=lambda p: "выбрать..." if p is None else names[p],
                 on_change=lambda: set_all(nonce, shown, src))
    st.caption("Или поправь каждое лицо отдельно. Исправления запоминаются: «Пересчитать кластеры» их не тронет.")

    cols = st.columns(5)
    for i, u in enumerate(shown):
        key = f"who_{nonce}_{i}"
        if key not in state:
            state[key] = u["person_id"] if u["person_id"] is not None else NOBODY
        with cols[i % 5], st.container(border=True, key=f"mount_who_{nonce}_{i}"):
            if u["thumb"]:
                st.image(square(u["thumb"], 128), width="stretch")
            st.caption(os.path.basename(u["path"]))
            st.selectbox("Кто это", opts, key=key, format_func=names.get, label_visibility="collapsed")

    targets = [state[f"who_{nonce}_{i}"] for i in range(len(shown))]
    bulk = state.get(f"who_bulk_{nonce}")
    hidden = units[WHO_MAX:]
    if hidden:  # not shown: only the quick "everyone" choice applies to them
        targets += [bulk if bulk is not None and in_scope(nonce, u, src) else u["person_id"] for u in hidden]
        st.caption(f"Ещё {len(hidden)} лиц не поместились — к ним применится только «Быстро: поставить всем сразу».")
    changes = {}
    for u, new in zip(units, targets):
        new = None if new == NOBODY else new
        if new != u["person_id"]:
            changes.setdefault(new, []).extend(u["face_ids"])
    if st.button(f"Сохранить ({sum(len(v) for v in changes.values())} лиц)" if changes else "Сохранить",
                 type="primary", disabled=not changes):
        before = []
        for pid, face_ids in changes.items():
            before += ps.assign_faces(con, face_ids, pid)
        state.setdefault("undo", []).append({"kind": "faces", "before": before})
        state.flash = (f"👤 Исправлено: {', '.join(names[p if p is not None else NOBODY] for p in changes)}"
                       f" · Ctrl+Z — вернуть")
        st.rerun()


def in_scope(nonce, u, src):
    return src is None or state.get(f"who_scope_{nonce}", "src") == "all" or u["person_id"] == src


def set_all(nonce, shown, src):
    """The quick "everyone" choice: set it on every face in scope."""
    pid = state.get(f"who_bulk_{nonce}")
    if pid is None:
        return
    for i, u in enumerate(shown):
        if in_scope(nonce, u, src):
            state[f"who_{nonce}_{i}"] = pid


def open_who(file_ids, src=None):
    state.who_nonce = state.get("who_nonce", 0) + 1  # fresh widgets every time the dialog opens
    who_dialog(file_ids, src)


def undo_last():
    stack = state.get("undo") or []
    if not stack:
        state.flash = "Нечего возвращать"
        return
    step = stack.pop()
    if step["kind"] == "collect":
        done, problems = ps.return_from_collection(con, step["pairs"])
        state.flash = f"Убрано из подборок: {done}"
    elif step["kind"] == "faces":
        ps.unassign_faces(con, step["before"])
        state.flash = f"Вернул как было: {len(step['before'])} лиц"
        return
    else:
        done, problems = ps.restore_deleted(con, step["ids"])
        state.flash = f"Возвращено: {done}"
    state.flash += "".join(f" · ⚠ {os.path.basename(p)}: {why}" for p, why in problems)


def copy_mode():
    return state.get("collect_mode") == "Копировать"


def collect_now(paths, cid=None, copy=None):
    """Move or copy (default: the sidebar switch) files into a collection (default: the current one in the sidebar);
    Ctrl+Z takes them out again. A collection chosen explicitly becomes the current one."""
    copy = copy_mode() if copy is None else copy
    if cid is not None:
        state._next_collection = cid
    else:
        cid = state.get("collection")
    if cid is None:
        cid = ps.create_collection(con, f"Подборка {datetime.now():%Y-%m-%d %H-%M}")
        state._next_collection = cid  # the sidebar selectbox is already drawn: switch on the next run
    name = con.execute("SELECT name FROM collections WHERE id = ?", (cid,)).fetchone()[0]
    with st.spinner(f"{'Копирую' if copy else 'Перемещаю'} {len(paths)} файл(ов) в «{name}»..."):
        done, failed, added = ps.move_to_collection(con, cid, paths, copy)
    if added:
        state.setdefault("undo", []).append({"kind": "collect", "pairs": added})
    state.flash = (f"{'📄 Скопировано' if copy else '📥 Перемещено'} в «{name}»: {done} · Ctrl+Z — вернуть"
                   + "".join(f" · ⚠ {os.path.basename(p)}: {why}" for p, why in failed))


@st.dialog("В подборку", width="large")
def collect_dialog(file_ids, paths):
    """Choose where to move/copy the selected files: a person on them, any collection, or a new one."""
    copy = st.radio("Что сделать", ["Переместить", "Скопировать"], index=int(copy_mode()), horizontal=True,
                    key="cd_mode", help="Скопировать — оригинал остаётся в архиве") == "Скопировать"
    verb = "Скопировать" if copy else "Переместить"
    q = ",".join("?" * len(file_ids))
    people = con.execute(f"""
        SELECT p.id, p.name, COUNT(DISTINCT x.file_id) FROM faces x JOIN persons p ON p.id = x.person_id
        WHERE x.file_id IN ({q}) AND p.name IS NOT NULL GROUP BY p.id ORDER BY 3 DESC, lower(p.name)""",
                         file_ids).fetchall()
    if people:
        st.markdown("**Люди на фото** — в подборку человека (создаётся сама):")
        cols = st.columns(4)
        for i, (pid, name, n) in enumerate(people):
            more = f" ({n}/{len(file_ids)})" if len(file_ids) > 1 else ""
            if cols[i % 4].button(f"📥 {name}{more}", key=f"cd_p_{pid}", width="stretch"):
                collect_now(paths, ps.collection_for_person(con, pid), copy)
                st.rerun()
    colls = dict(con.execute("SELECT id, name FROM collections ORDER BY created_at DESC"))
    if colls:
        c1, c2 = st.columns([3, 1], vertical_alignment="bottom")
        cid = c1.selectbox("Любая подборка", list(colls), format_func=colls.get, index=None,
                           placeholder="начни вводить название")
        if c2.button(f"📥 {verb}", disabled=cid is None, width="stretch", key="cd_any"):
            collect_now(paths, cid, copy)
            st.rerun()
    c1, c2 = st.columns([3, 1], vertical_alignment="bottom")
    new = c1.text_input("Новая подборка", placeholder="Название")
    if c2.button(f"➕ Создать и {verb.lower()}", disabled=not new.strip(), width="stretch"):
        collect_now(paths, ps.create_collection(con, new.strip()), copy)
        st.rerun()


def collect_by_people_now(file_ids):
    """Each file into the collections of all named people on it."""
    with st.spinner(f"Раскладываю {len(file_ids)} файл(ов) по людям..."):
        per_person, nobody, failed, added = ps.collect_by_people(con, file_ids, copy_mode())
    if added:
        state.setdefault("undo", []).append({"kind": "collect", "pairs": added})
    parts = [f"{name}: {n}" for name, n in sorted(per_person.items(), key=lambda kv: -kv[1])]
    state.flash = ("👥 Разложено — " + (", ".join(parts) if parts else "никуда")
                   + (f" · без подписанных людей: {len(nobody)} (подпиши людей или используй 📥)" if nobody else "")
                   + (" · Ctrl+Z — вернуть" if added else "")
                   + "".join(f" · ⚠ {os.path.basename(p)}: {why}" for p, why in failed))


def file_grid(key, rows, extra_actions=(), videos=None, collect=True, person=None):
    """Explorer-like grid of files with delete actions.

    rows: [{"id", "path", "kind", "cap", "img" (cache key), "get_img"}] — the full filtered list (rendered lazily).
    videos: show videos too; default = the sidebar toggle (off), so a bulk delete never hits videos by accident.
    collect: offer "move to the current collection".
    person: the person whose page this is ("Кто на фото" fixes their faces first).
    Handles delete / collect itself; returns any other action ({"name", "ids"}) or None.
    """
    # fixed slots above the grid: messages must not shift it, or Streamlit remounts it (selection lost)
    flash_slot, note_slot = st.empty(), st.empty()
    if msg := state.pop("flash", None):
        flash_slot.success(msg)
    if videos is None:
        videos = state.get("show_video", False)
    notes = []
    if not videos:
        n_hidden = sum(r["kind"] == "video" for r in rows)
        rows = [r for r in rows if r["kind"] != "video"]
        if n_hidden:
            notes.append(f"🎬 Скрыто видео: {n_hidden} — они в разделе «Видео» или включи «Показывать и видео» слева.")
    hide_collected = collect and state.get("hide_collected", True)
    if hide_collected:
        n_coll = sum(r["id"] in IN_COLLECTION for r in rows)
        rows = [r for r in rows if r["id"] not in IN_COLLECTION]
        if n_coll:
            notes.append(f"📥 Скрыто уже в подборках: {n_coll} — выключи «Скрывать то, что в подборках» слева.")
    if notes:
        note_slot.caption("  \n".join(notes))
    if not rows and state.get("undo"):  # nothing left to hover for Ctrl+Z (e.g. everything was just deleted)
        if st.button("↩ Вернуть последнее (Ctrl+Z)", key=f"undo_{key}"):
            undo_last()
            st.rerun()
    if any("group" in r for r in rows):  # the header goes on the first visible item of each group
        seen, out = set(), []
        for r in rows:
            first = r["group"] not in seen
            seen.add(r["group"])
            out.append(dict(r, head=r["group_head"] if first else None))
        rows = out
    key = f"{key}_v{int(videos)}_h{int(hide_collected)}"
    lid, limit = _list_id(key), shown(key)
    paths = {r["id"]: r["path"] for r in rows}
    actions = list(extra_actions)
    if collect:
        target = current_collection_name() or "новую подборку"
        into = f"📄 Копия в «{target}»" if copy_mode() else f"📥 В «{target}»"
        actions[:0] = [
            {"name": "collect", "label": into, "label_sel": f"{into} ({{n}})", "need_sel": True, "viewer": into},
            {"name": "collect_to", "label": "📥 В подборку…", "label_sel": "📥 В подборку… ({n})", "need_sel": True,
             "viewer": "📥 Куда…", "stay": True},
            {"name": "by_people", "label": "👥 По людям", "label_sel": "👥 По людям ({n})", "need_sel": True,
             "viewer": "👥 По людям"},
        ]
    actions.append({"name": "who", "label": "👤 Кто на фото", "label_sel": "👤 Кто на фото ({n})", "need_sel": True,
                    "viewer": "👤 Кто это", "stay": True})
    if not any(ps.is_protected(p) for p in paths.values()):
        actions = [{"name": "delete", "label": "🗑 Удалить выбранные", "label_sel": "🗑 Удалить выбранные ({n})",
                    "need_sel": True, "danger": True}] + actions
    else:
        note_slot.caption("🔒 Папка защищена (protected.txt), удаление выключено")
    act = photo_grid(lid, rows[:limit], len(rows), limit < len(rows), actions, on_more=_bump(lid, limit),
                     media=media_server_for(DB))
    if not act:
        return None
    if act["name"] == "delete":
        delete_files([paths[i] for i in act["ids"] if i in paths])
    elif act["name"] == "quick_delete":  # Ctrl+D in the viewer; ids already gone are skipped
        todo = [paths[i] for i in act["ids"] if i in paths]
        if todo and not any(ps.is_protected(p) for p in todo):
            delete_now(todo, one_by_one=True)
            st.rerun()
    elif act["name"] == "undo":
        undo_last()
        st.rerun()
    elif act["name"] == "collect":
        todo = [paths[i] for i in act["ids"] if i in paths]
        if todo:
            collect_now(todo)
        st.rerun()
    elif act["name"] == "who":
        todo = [i for i in act["ids"] if i in paths]
        if todo:
            open_who(todo, person)
    elif act["name"] == "collect_to":
        todo = [i for i in act["ids"] if i in paths]
        if todo:
            collect_dialog(todo, [paths[i] for i in todo])
    elif act["name"] == "by_people":
        todo = [i for i in act["ids"] if i in paths]
        if todo:
            collect_by_people_now(todo)
        st.rerun()
    else:
        return act
    return None


def in_collection():
    """{file_id: "name1, name2"} for files currently in collections."""
    return dict(con.execute("SELECT c.file_id, group_concat(k.name, ', ') FROM collection_files c "
                            "JOIN collections k ON k.id = c.collection_id GROUP BY c.file_id"))


def current_collection_name():
    cid = state.get("collection")
    row = con.execute("SELECT name FROM collections WHERE id = ?", (cid,)).fetchone() if cid else None
    return row[0] if row else None


def preview_row(fid, path, kind, taken_at, place, extra="", coll=True):
    """coll: show the "📥 collections" line."""
    who = PEOPLE_IN.get(fid)
    coll = IN_COLLECTION.get(fid) if coll else None
    return {"id": fid, "path": path, "kind": kind, "img": f"p{fid}", "get_img": lambda: preview(fid),
            "cap": (f"👤 {who}\n" if who else "") + (f"📥 {coll}\n" if coll else "")
                   + file_caption(path, kind, taken_at, place) + extra,
            "title": path}


def people_in():
    """{file_id: "Петя, Вася +2"}: named people on each file, +N = other faces (unnamed / strangers;
    in videos only grouped ones — a video has many stray faces)."""
    rows = con.execute("""
        SELECT g.file_id, group_concat(g.name, ', '), SUM(g.name IS NULL)
        FROM (SELECT x.file_id, p.name FROM faces x JOIN files f ON f.id = x.file_id
              LEFT JOIN persons p ON p.id = x.person_id
              WHERE x.person_id IS NOT NULL OR f.kind != 'video'
              GROUP BY x.file_id, COALESCE(x.person_id, -x.id)) g
        GROUP BY g.file_id""")
    return {fid: ", ".join(x for x in [names, f"+{n}" if n else ""] if x) for fid, names, n in rows}


def save_name(pid):
    """on_change of the name field on a person card."""
    name = state[f"nm_{pid}"].strip()
    con.execute("UPDATE persons SET name = ? WHERE id = ?", (name or None, pid))
    con.commit()


# ---------------------------------------------------------------- people overview

def people_page():
    url_page("people")
    url_init("pq", "q", "")
    url_init("pmin", "min", 2, int)
    url_init("phid", "hidden", False, lambda v: v == "1")
    c1, c2, c3 = st.columns([3, 1, 1])
    q = c1.text_input("Поиск по имени", placeholder="имя или номер", key="pq")
    min_files = c2.number_input("Мин. файлов", 1, 10000, key="pmin")
    show_hidden = c3.toggle("Скрытые", key="phid")
    url_set("q", q, "")
    url_set("min", min_files, 2)
    url_set("hidden", "1" if show_hidden else "", "")

    # with videos hidden, count photos only: a person seen only in videos would open as an empty page
    videos = state.get("show_video", False)
    counts = f"""WITH pc AS (
        SELECT x.person_id AS pid, COUNT(DISTINCT x.file_id) AS n FROM faces x JOIN files f ON f.id = x.file_id
        WHERE x.person_id IS NOT NULL {'' if videos else "AND f.kind = 'image'"} GROUP BY x.person_id)"""
    where = "pc.n >= ? AND p.hidden = ?"
    params = [min_files, int(show_hidden)]
    if q:
        where += " AND (lower(p.name) LIKE lower(?) OR CAST(p.id AS TEXT) = ?)"
        params += [f"%{q}%", q]
    total = con.execute(f"{counts} SELECT COUNT(*) FROM persons p JOIN pc ON pc.pid = p.id WHERE {where}",
                        params).fetchone()[0]
    key = f"people_{q}_{min_files}_{show_hidden}_{videos}"
    rows = con.execute(f"""{counts}
        SELECT p.id, p.name, pc.n,
               (SELECT thumb FROM faces WHERE person_id = p.id AND thumb IS NOT NULL
                ORDER BY size * det_score DESC LIMIT 1),
               (SELECT substr(MIN(f.taken_at), 1, 4) || '–' || substr(MAX(f.taken_at), 1, 4)
                FROM faces x JOIN files f ON f.id = x.file_id WHERE x.person_id = p.id)
        FROM persons p JOIN pc ON pc.pid = p.id WHERE {where} ORDER BY pc.n DESC, p.id LIMIT ?
    """, params + [shown(key)]).fetchall()
    unit = "файлов" if videos else "фото"

    selected = [pid for pid, *_ in rows if state.get(f"sel_{pid}")]
    a1, a2, a3, _ = st.columns([2, 2, 3, 3])
    if a3.button(f"Файлы только с выбранными ({len(selected)})", disabled=not selected,
                 help="Фото и видео, где в кадре только отмеченные люди — и никого больше"):
        for pid in selected:
            state.pop(f"sel_{pid}", None)
        state.pop("group_people", None)
        st.switch_page(PAGES["group"], query_params={"ids": ",".join(map(str, selected))})
    if a1.button(f"Объединить ({len(selected)})", disabled=len(selected) < 2,
                 help="Все отмеченные — один человек. Остаётся первый (у кого больше файлов)"):
        ps.merge_persons(con, selected)
        for pid in selected:
            state.pop(f"sel_{pid}", None)
        st.rerun()
    if a2.button(f"{'Показать' if show_hidden else 'Скрыть'} ({len(selected)})", disabled=not selected):
        con.executemany("UPDATE persons SET hidden = ? WHERE id = ?", [(int(not show_hidden), p) for p in selected])
        con.commit()
        for pid in selected:
            state.pop(f"sel_{pid}", None)
        st.rerun()

    cols = st.columns(6)
    for i, (pid, name, nf, thumb, years) in enumerate(rows):
        with cols[i % 6], st.container(border=True, key=f"mount_p_{pid}"):
            if thumb:
                st.image(square(thumb), width="stretch")
            if state.get(f"nm_{pid}") != (name or ""):
                state[f"nm_{pid}"] = name or ""  # keep the field in sync with renames made elsewhere
            st.text_input("Имя", key=f"nm_{pid}", placeholder=f"person_{pid}", on_change=save_name, args=(pid,),
                          label_visibility="collapsed")
            st.caption(f"#{pid} · {nf} {unit} · {years or ''}")
            b1, b2 = st.columns([1, 3], vertical_alignment="center")
            b1.checkbox("выбрать", key=f"sel_{pid}", label_visibility="collapsed")
            b2.page_link(PAGES["person"], label="Открыть", query_params={"id": pid}, width="stretch")
    load_more(key, total)
    url_done("people")


# ---------------------------------------------------------------- one person

PERSON_MODES = {  # url code -> (label, HAVING clause)
    "all": ("все", ""),
    "alone": ("только он(а) в кадре", "HAVING foreign_faces = 0"),  # no other face at all, even strangers
    "noknown": ("без других знакомых", "HAVING others = 0"),        # strangers in the background allowed
    "with": ("с другими", "HAVING others > 0"),
}


def person_page():
    st.page_link(PAGES["people"], label="← Ко всем людям")
    if (to := state.pop("_goto_person", None)) is not None:
        st.query_params.from_dict({"id": str(to)})
    try:
        pid = int(st.query_params.get("id", ""))
    except ValueError:
        pid = None
    row = con.execute("SELECT name, n_files, hidden, skip_collect FROM persons WHERE id = ?",
                      (pid,)).fetchone() if pid else None
    if not row:
        st.warning("Такого человека нет (возможно, его объединили с другим или пересчитали кластеры).")
        return
    name, nf, hidden, skip_collect = row

    c1, c2, c3 = st.columns([3, 1, 1])
    new = c1.text_input("Имя", value=name or "", placeholder=f"person_{pid}")
    if new != (name or ""):
        con.execute("UPDATE persons SET name = ? WHERE id = ?", (new or None, pid))
        con.commit()
        st.rerun()
    c2.metric("Файлов", nf)
    if c3.button("Показать" if hidden else "Скрыть"):
        con.execute("UPDATE persons SET hidden = ? WHERE id = ?", (int(not hidden), pid))
        con.commit()
        st.rerun()
    skip = st.toggle("Не раскладывать по подборкам (это я)", value=bool(skip_collect), key=f"skip_{pid}",
                     help="«👥 По людям» не будет создавать подборку для этого человека")
    if skip != bool(skip_collect):
        con.execute("UPDATE persons SET skip_collect = ? WHERE id = ?", (int(skip), pid))
        con.commit()
    with st.popover("🔗 Это на самом деле другой человек…"):
        st.caption(f"Все лица «{label(pid, name)}» ({nf} файлов) перейдут к выбранному человеку. Ctrl+Z — вернуть.")
        others = con.execute("SELECT id, name FROM persons WHERE id != ? AND (name IS NOT NULL OR n_files >= 3) "
                             "ORDER BY name IS NULL, lower(name), n_files DESC", (pid,)).fetchall()
        onames = {o: label(o, n) for o, n in others}
        to = st.selectbox("Кто это", list(onames), format_func=onames.get, index=None, key=f"remap_{pid}",
                          placeholder="начни вводить имя")
        new_to = st.text_input("или новый человек", key=f"remap_new_{pid}", placeholder="Имя")
        if st.button("Перенести всё", type="primary", disabled=to is None and not new_to.strip()):
            to = ps.create_person(con, new_to.strip()) if new_to.strip() else to
            face_ids = [i for (i,) in con.execute("SELECT id FROM faces WHERE person_id = ?", (pid,))]
            state.setdefault("undo", []).append({"kind": "faces", "before": ps.assign_faces(con, face_ids, to)})
            state.flash = f"🔗 «{label(pid, name)}» → «{label(to, onames.get(to) or new_to.strip())}»: {len(face_ids)} лиц · Ctrl+Z — вернуть"
            state._goto_person = to  # the URL must change in a normal run, not right before st.rerun()
            st.rerun()
    if not name:
        st.caption("👥 «По людям» раскладывает только по подписанным людям — дай имя, чтобы у него была своя подборка.")

    f1, f2 = st.columns([3, 1])
    url_init(f"mode_{pid}", "mode", "all", lambda v: v if v in PERSON_MODES else "all")
    url_init(f"whole_{pid}", "whole", True, lambda v: v != "0")
    mode = f1.radio("Файлы", list(PERSON_MODES), format_func=lambda m: PERSON_MODES[m][0], horizontal=True,
                    key=f"mode_{pid}",
                    help="«только он(а) в кадре» — на фото/видео нет ни одного другого лица, даже незнакомого")
    whole = f2.toggle("Целые фото", help="Показывать превью всего кадра, а не только лицо", key=f"whole_{pid}")
    url_set("mode", mode, "all")
    url_set("whole", "" if whole else "0", "")

    # people who appear together with this person, most frequent first
    co = con.execute("""
        SELECT p.id, p.name, COUNT(DISTINCT x.file_id) FROM faces x JOIN persons p ON p.id = x.person_id
        WHERE x.file_id IN (SELECT file_id FROM faces WHERE person_id = ?) AND p.id != ?
        GROUP BY p.id ORDER BY 3 DESC
    """, (pid, pid)).fetchall()
    co_names = {cid: f"{label(cid, cname)} · вместе {n}" for cid, cname, n in co}
    url_init(f"co_{pid}", "with", [], lambda v: [i for i in ids_param(v) if i in co_names])
    with_people = st.multiselect("С кем в кадре", list(co_names), format_func=co_names.get, key=f"co_{pid}",
                                 placeholder="все файлы; выбери людей, чтобы оставить только файлы, где они тоже есть")
    url_set("with", with_people, "")
    if with_people and mode == "alone":
        st.warning("«только он(а) в кадре» и «с кем в кадре» вместе ничего не найдут — выбери другой режим.")
    co_cond = "".join(" AND f.id IN (SELECT file_id FROM faces WHERE person_id = ?)" for _ in with_people)

    rows = con.execute(f"""
        SELECT f.id, f.path, f.kind, f.taken_at, f.place, fa.thumb, fa.t_sec, MAX(fa.size),
               (SELECT COUNT(DISTINCT person_id) FROM faces
                WHERE file_id = f.id AND person_id IS NOT NULL AND person_id != ?) AS others,
               (SELECT group_concat(DISTINCT COALESCE(p.name, 'person_' || p.id)) FROM faces x
                JOIN persons p ON p.id = x.person_id WHERE x.file_id = f.id AND p.id != ?) AS with_whom,
               (SELECT COUNT(*) FROM faces WHERE file_id = f.id AND (person_id IS NULL OR person_id != ?)) AS foreign_faces,
               (SELECT COUNT(*) FROM faces WHERE file_id = f.id AND person_id IS NULL) AS strangers
        FROM faces fa JOIN files f ON f.id = fa.file_id
        WHERE fa.person_id = ?{co_cond} GROUP BY f.id {PERSON_MODES[mode][1]} ORDER BY f.taken_at, f.path
    """, (pid, pid, pid, pid, *with_people)).fetchall()

    items = []
    for fid, path, kind, taken_at, place, thumb, t, _, others, with_whom, _, strangers in rows:
        it = preview_row(fid, path, kind, taken_at, place)
        if kind == "video" and t is not None:
            it["cap"] = it["cap"].replace(os.path.basename(path), f"{os.path.basename(path)} · {t:.0f}s", 1)
        if not whole:  # the face of this person instead of the whole frame
            it["img"], it["get_img"] = f"f{pid}_{fid}", (lambda th=thumb: th)
        items.append(it)

    act = file_grid(f"pp_{pid}_{mode}_{sorted(with_people)}_{whole}", items, person=pid, extra_actions=[
        {"name": "unlink", "label": "Это не он(а)", "label_sel": "Это не он(а): отвязать ({n})", "need_sel": True}])
    if act and act["name"] == "unlink" and act["ids"]:
        q = ",".join("?" * len(act["ids"]))
        face_ids = [i for (i,) in con.execute(f"SELECT id FROM faces WHERE person_id = ? AND file_id IN ({q})",
                                              [pid, *act["ids"]])]
        if face_ids:
            state.setdefault("undo", []).append({"kind": "faces", "before": ps.assign_faces(con, face_ids, None)})
            state.flash = f"Отвязано от {label(pid, name)}: {len(act['ids'])} файл(ов) · Ctrl+Z — вернуть"
        st.rerun()
    url_done(f"person{pid}")


# ---------------------------------------------------------------- files with only the chosen people

def group_page():
    url_page("group")
    st.subheader("Файлы, где в кадре только выбранные люди")
    people = con.execute("SELECT id, name, n_files FROM persons WHERE n_faces > 0 ORDER BY name IS NULL, n_files DESC").fetchall()
    names = {pid: f"{label(pid, name)} · {nf} файлов" for pid, name, nf in people}
    url_init("group_people", "ids", [], ids_param)
    state.group_people = [p for p in state.group_people if p in names]  # drop merged/removed people
    url_init("g_all", "all", False, lambda v: v == "1")
    url_init("g_str", "strangers", False, lambda v: v == "1")
    url_init("g_whole", "whole", True, lambda v: v == "1")
    chosen = st.multiselect("Люди", list(names), format_func=names.get, key="group_people",
                            placeholder="начни вводить имя или номер person_…")
    c1, c2, c3 = st.columns([2, 2, 2])
    need_all = c1.toggle("Все выбранные в кадре", key="g_all",
                         help="Только файлы, где есть каждый из выбранных (например, вы вдвоём)")
    allow_strangers = c2.toggle("Разрешить незнакомых на фоне", key="g_str",
                                help="Выключено: в кадре нет ни одного лица, кроме выбранных людей")
    whole = c3.toggle("Целые фото", key="g_whole")
    url_set("ids", chosen, "")
    url_set("all", "1" if need_all else "", "")
    url_set("strangers", "1" if allow_strangers else "", "")
    url_set("whole", "" if whole else "0", "")
    if not chosen:
        st.info("Выбери одного или нескольких людей.")
        url_done("group")
        return

    q = ",".join("?" * len(chosen))
    cond = [f"f.id IN (SELECT file_id FROM faces WHERE person_id IN ({q}))",
            f"NOT EXISTS (SELECT 1 FROM faces o WHERE o.file_id = f.id AND o.person_id IS NOT NULL "
            f"AND o.person_id NOT IN ({q}))"]
    params = chosen + chosen
    if not allow_strangers:
        cond.append("NOT EXISTS (SELECT 1 FROM faces o WHERE o.file_id = f.id AND o.person_id IS NULL)")
    if need_all:
        cond.append(f"(SELECT COUNT(DISTINCT person_id) FROM faces WHERE file_id = f.id AND person_id IN ({q})) = ?")
        params += chosen + [len(chosen)]
    rows = con.execute(f"""
        SELECT f.id, f.path, f.kind, f.taken_at, f.place,
               (SELECT group_concat(DISTINCT COALESCE(p.name, 'person_' || p.id)) FROM faces x
                JOIN persons p ON p.id = x.person_id WHERE x.file_id = f.id),
               (SELECT COUNT(*) FROM faces x WHERE x.file_id = f.id AND x.person_id IS NULL),
               (SELECT thumb FROM faces x WHERE x.file_id = f.id ORDER BY size DESC LIMIT 1)
        FROM files f WHERE {' AND '.join(cond)} ORDER BY f.taken_at, f.path
    """, params).fetchall()

    n_video = sum(r[2] == "video" for r in rows)
    st.caption(f"Найдено {len(rows)} файлов ({len(rows) - n_video} фото, {n_video} видео).")
    items = []
    for fid, path, kind, taken_at, place, who, strangers, thumb in rows:
        it = preview_row(fid, path, kind, taken_at, place)
        if not whole:
            it["img"], it["get_img"] = f"g{fid}", (lambda th=thumb: th)
        items.append(it)
    file_grid(f"group_{sorted(chosen)}_{need_all}_{allow_strangers}_{whole}", items)
    url_done("group")


# ---------------------------------------------------------------- files without people / all files

def files_page(page, view, title, only_video=False):
    url_page(page)
    st.subheader(title)
    kind_cond = "kind = 'video'" if only_video else "1"
    years = [y for (y,) in con.execute(
        f"SELECT DISTINCT substr(taken_at, 1, 4) FROM {view} WHERE {kind_cond} ORDER BY 1 DESC") if y]
    cats = dict(con.execute(
        f"SELECT label, COUNT(*) FROM tags WHERE rank = 1 AND file_id IN (SELECT id FROM {view} WHERE {kind_cond}) "
        f"GROUP BY label ORDER BY 2 DESC"))
    url_init(f"{page}_year", "year", "все", lambda v: v if v in years else "все")
    url_init(f"{page}_cat", "cat", "все", lambda v: v if v in cats else "все")
    url_init(f"{page}_place", "place", "")
    c1, c2, c3 = st.columns([1, 2, 2])
    year = c1.selectbox("Год", ["все"] + years, key=f"{page}_year")
    cat = c2.selectbox("Категория", ["все"] + list(cats), key=f"{page}_cat",
                       format_func=lambda c: c if c == "все" else f"{c} ({cats[c]})")
    place = c3.text_input("Место", placeholder="город или страна", key=f"{page}_place")
    url_set("year", year, "все")
    url_set("cat", cat, "все")
    url_set("place", place, "")

    where, params = [kind_cond], []
    if cat != "все":
        where.append("id IN (SELECT file_id FROM tags WHERE rank = 1 AND label = ?)")
        params.append(cat)
    if year != "все":
        where.append("substr(taken_at, 1, 4) = ?")
        params.append(year)
    if place:
        where.append("lower(place) LIKE lower(?)")
        params.append(f"%{place}%")
    rows = con.execute(f"""
        SELECT id, path, kind, taken_at, place,
               (SELECT label FROM tags WHERE file_id = v.id AND rank = 1)
        FROM {view} v WHERE {' AND '.join(where)} ORDER BY taken_at, path
    """, params).fetchall()
    file_grid(f"{view}_{only_video}_{year}_{cat}_{place}",
              [preview_row(fid, path, k, taken_at, pl, f"\n🏷 {tag}" if tag else "")
               for fid, path, k, taken_at, pl, tag in rows],
              videos=True if only_video else None)
    url_done(page)


def all_files_page():
    files_page("files", "files", "Все фото и видео")


def no_people_page():
    files_page("nopeople", "files_without_people", "Фото и видео без людей")


def strangers_page():
    files_page("strangers", "files_strangers_only", "Только незнакомые / мелкие лица")


def videos_page():
    files_page("videos", "files", "Видео", only_video=True)


# ---------------------------------------------------------------- stacks of similar photos

def stack_caption(rank, m):
    if m["score"] is None:
        return "оценка не посчитана"
    bits = [f"резкость {m['sharp']:.0f}"] if m["sharp"] is not None else []
    if m["face_sharp"] is not None:
        bits.append(f"лица {m['face_sharp']:.0f}")
    head = "⭐ лучший" if rank == 0 else f"#{rank + 1} · {m['score']:.0f}"
    return head + (" · " + " · ".join(bits) if bits else "")


def stacks_page():
    url_page("stacks")
    st.subheader("Стопки похожих фото")
    st.caption("Серии и повторные дубли одного момента (в т.ч. с разных телефонов и фотоаппарата) и копии одного кадра. "
               "Внутри стопки лучший — первым (⭐). Похожесть определяет нейросеть SigLIP2, время съёмки — подсказка.")
    if not con.execute("SELECT 1 FROM clip LIMIT 1").fetchone():
        st.info("Сначала посчитай векторы картинок: `python scenes.py embed`")
        return
    n_stacks, n_photos = con.execute("SELECT COUNT(DISTINCT stack_id), COUNT(*) FROM stacks").fetchone()
    n_todo = con.execute("SELECT COUNT(*) FROM stacks s WHERE NOT EXISTS "
                         "(SELECT 1 FROM quality q WHERE q.file_id = s.file_id)").fetchone()[0]
    c1, c2 = st.columns(2)
    if c1.button("🔄 Собрать стопки заново" if n_stacks else "Собрать стопки", type="secondary" if n_stacks else "primary"):
        bar = st.progress(0.0, "Ищу похожие фото...")
        k, n = stacks.build(con, progress=lambda f: bar.progress(min(1.0, f), "Ищу похожие фото..."))
        state.flash = f"Стопок: {k}, фото в них: {n}"
        st.rerun()
    if n_todo and c2.button(f"⭐ Оценить качество ({n_todo} фото, ~{max(1, n_todo // 1500)} мин)", type="primary",
                            help="Читает оригиналы (только чтение): резкость кадра и лиц, экспозиция, «красота» по нейросети"):
        bar = st.progress(0.0, "Оцениваю...")
        stacks.score(con, workers=12, progress=lambda f: bar.progress(min(1.0, f), f"Оцениваю... {f:.0%}"))
        st.rerun()
    if not n_stacks:
        st.info("Стопок ещё нет — нажми «Собрать стопки» (около минуты).")
        url_done("stacks")
        return

    st.page_link(PAGES["copies"], label="Копии одного кадра (оставить по одной) — в разделе «Копии»", icon="👯")
    url_init("st_year", "year", "все")
    years = ["все"] + [y for (y,) in con.execute("SELECT DISTINCT substr(f.taken_at, 1, 4) FROM stacks s "
                                                  "JOIN files f ON f.id = s.file_id ORDER BY 1 DESC")]
    kind = "series"
    year = st.columns([3, 1])[1].selectbox("Год", years, key="st_year")
    url_set("year", year, "все")
    where, params = ["s.copies = 0"], []
    if year != "все":
        where.append("substr(f.taken_at, 1, 4) = ?")
        params.append(year)
    all_stacks = stacks.load_stacks(con, ("WHERE " + " AND ".join(where)) if where else "", params)

    items, group_of, best = [], {}, {}
    for sid, copies, members in all_stacks:
        m0 = members[0]
        cams = sorted({m["camera"] for m in members if m["camera"]})
        head = (f"{(m0['taken_at'] or '')[:16].replace('T', ' ')} · {len(members)} фото · "
                f"{'копии одного кадра' if copies else 'серия'}" + (f" · {', '.join(cams)}" if len(cams) > 1 else ""))
        best[sid] = m0["id"] if m0["score"] is not None else None
        for rank, m in enumerate(members):
            it = preview_row(m["id"], m["path"], m["kind"], m["taken_at"], m["place"])
            it["cap"] = stack_caption(rank, m) + "\n" + it["cap"]
            it["group"], it["group_head"] = sid, head
            group_of[m["id"]] = sid
            items.append(it)
    n_extra = sum(len(m) - 1 for sid, _, m in all_stacks if best[sid] is not None)
    st.caption(f"{len(all_stacks)} стопок, {len(items)} фото · если оставить только лучшие, уйдёт {n_extra}"
               + (f" · ещё {n_todo} фото без оценки" if n_todo else ""))

    act = file_grid(f"stacks_{kind}_{year}", items, extra_actions=[
        {"name": "keep_best", "label": "⭐ Оставить только лучшие во всех стопках",
         "label_sel": "⭐ Оставить только лучшие в стопках выбранных ({n})", "danger": True},
        {"name": "keep_sel", "label": "✅ Оставить выбранные, остальное из их стопок удалить",
         "label_sel": "✅ Оставить выбранные ({n}), остальное из их стопок удалить", "need_sel": True, "danger": True},
    ])
    if act and act["name"] in ("keep_best", "keep_sel"):
        keep_only(act, {sid: ms for sid, _, ms in all_stacks}, best, group_of)
    url_done("stacks")


def keep_only(act, groups, keepers, group_of):
    """'Keep the best/selected' actions of the stacks and copies pages: confirm deleting the rest.
    groups: {group: [file ids]}, keepers: {group: file id to keep or None}."""
    paths = {m["id"]: m["path"] for ms in groups.values() for m in ms}
    chosen = set(act["ids"])
    gids = {group_of[i] for i in chosen if i in group_of} if chosen else set(groups)
    if act["name"] == "keep_best":
        gids = {g for g in gids if keepers.get(g) is not None}
        keep = {keepers[g] for g in gids}
    else:
        keep = chosen
    doomed = [paths[m["id"]] for g in gids for m in groups[g] if m["id"] not in keep]
    if doomed:
        confirm_delete(doomed)
    else:
        state.flash = "Нечего удалять"
        st.rerun()


def copies_page():
    url_page("copies")
    st.subheader("Копии одного кадра")
    st.caption("Один и тот же снимок несколько раз: `X.jpg` и `X(0).jpg`, HEIC и его JPEG, RAW+JPEG, уменьшенные и "
               "пересланные копии. Можно оставить по одной — первая в группе (⭐) остаётся.")
    if not con.execute("SELECT 1 FROM clip LIMIT 1").fetchone():
        st.info("Сначала посчитай векторы картинок: `python scenes.py embed`")
        return
    if not con.execute("SELECT 1 FROM copies LIMIT 1").fetchone():
        if st.button("Найти копии", type="primary"):
            bar = st.progress(0.0, "Ищу копии...")
            stacks.build(con, progress=lambda f: bar.progress(min(1.0, f), "Ищу копии..."))
            st.rerun()
        url_done("copies")
        return

    url_init("cp_kind", "kind", "all")
    url_init("cp_pref", "keep", "jpeg")
    url_init("cp_year", "year", "все")
    years = ["все"] + [y for (y,) in con.execute("SELECT DISTINCT substr(f.taken_at, 1, 4) FROM copies c "
                                                  "JOIN files f ON f.id = c.file_id ORDER BY 1 DESC")]
    f1, f2, f3 = st.columns([3, 3, 1])
    kind = f1.radio("Какие", ["all", "same", "diff"], horizontal=True, key="cp_kind",
                    format_func={"all": "все", "same": "точные дубли", "diff": "разные форматы / размеры"}.get,
                    help="«Точные дубли» — одинаковые разрешение и размер файла: удалять безопаснее всего")
    prefer = f2.radio("Если кадр одинаковый, оставлять", ["jpeg", "original"], horizontal=True, key="cp_pref",
                      format_func={"jpeg": "JPEG (открывается везде)", "original": "оригинал HEIC / RAW"}.get)
    year = f3.selectbox("Год", years, key="cp_year")
    url_set("kind", kind, "all")
    url_set("keep", prefer, "jpeg")
    url_set("year", year, "все")
    where, params = ("WHERE substr(f.taken_at, 1, 4) = ?", [year]) if year != "все" else ("", [])
    groups = [g for g in stacks.load_copies(con, where, params, prefer)
              if kind == "all" or g[1][0]["identical"] == (kind == "same")]

    items, group_of, keepers = [], {}, {}
    for gid, members in groups:
        m0 = members[0]
        sizes = sorted({f"{m['w']}×{m['h']}" for m in members})
        head = (f"{(m0['taken_at'] or '')[:16].replace('T', ' ')} · {len(members)} копии"
                + (" · одинаковые файлы" if m0["identical"] else f" · {', '.join(sizes)}"))
        keepers[gid] = m0["id"]
        for rank, m in enumerate(members):
            it = preview_row(m["id"], m["path"], m["kind"], m["taken_at"], m["place"])
            ext = os.path.splitext(m["path"])[1].upper().lstrip(".")
            it["cap"] = (("⭐ оставить" if rank == 0 else "копия")
                         + f" · {m['w']}×{m['h']} · {m['size'] / 2**20:.1f} МБ · {ext}\n" + it["cap"])
            it["group"], it["group_head"] = gid, head
            group_of[m["id"]] = gid
            items.append(it)
    st.caption(f"{len(groups)} групп, {len(items)} файлов · если оставить по одному, уйдёт {len(items) - len(groups)} "
               f"({sum(m['size'] for _, ms in groups for m in ms[1:]) / 2**30:.1f} ГБ)")
    act = file_grid(f"copies_{kind}_{prefer}_{year}", items, extra_actions=[
        {"name": "keep_best", "label": "⭐ Оставить по одному во всех группах",
         "label_sel": "⭐ Оставить по одному в группах выбранных ({n})", "danger": True},
        {"name": "keep_sel", "label": "✅ Оставить выбранные, остальные копии удалить",
         "label_sel": "✅ Оставить выбранные ({n}), остальные копии удалить", "need_sel": True, "danger": True},
    ])
    if act and act["name"] in ("keep_best", "keep_sel"):
        keep_only(act, dict(groups), keepers, group_of)
    url_done("copies")


# ---------------------------------------------------------------- text search

@st.cache_resource(show_spinner="Загружаю модель...")
def text_model():
    return scenes.Clip(image=False)


@st.cache_resource(show_spinner=False)
def image_index(n_rows):  # n_rows is the cache key: recomputed after `scenes.py embed`
    return scenes.file_embeddings(con)


def search_page():
    url_page("search")
    st.subheader("Поиск по содержимому")
    n = con.execute("SELECT COUNT(*) FROM clip").fetchone()[0]
    if not n:
        st.info("Сначала посчитай векторы: `python scenes.py embed`")
        return
    url_init("s_q", "q", "")
    url_init("s_np", "nopeople", False, lambda v: v == "1")
    c1, c2 = st.columns([4, 1])
    q = c1.text_input("Что ищем", placeholder="закат на море, торт со свечами, кошка на диване, паспорт...", key="s_q")
    only_no_people = c2.toggle("Только без людей", key="s_np")
    url_set("q", q, "")
    url_set("nopeople", "1" if only_no_people else "", "")
    if not q:
        url_done("search")
        return
    fids, M = image_index(n)
    items = []
    for fid, s in scenes.search(con, text_model(), q, limit=600, fids=fids, M=M):
        r = con.execute("SELECT path, kind, taken_at, place, n_faces FROM files WHERE id = ?", (fid,)).fetchone()
        if r and not (only_no_people and r[4]):
            it = preview_row(fid, *r[:4])
            it["cap"] = f"{s:.3f} · " + it["cap"]
            items.append(it)
    file_grid(f"search_{q}_{only_no_people}", items)
    url_done("search")


# ---------------------------------------------------------------- similar people (same person split in two?)

@st.cache_data(show_spinner="Сравниваю людей...", max_entries=4)
def person_centroids(signature):  # signature: changes whenever faces/persons change
    rows = con.execute("SELECT person_id, embedding FROM faces WHERE person_id IS NOT NULL ORDER BY person_id").fetchall()
    pids = np.array([r[0] for r in rows])
    E = ps.from_blobs([r[1] for r in rows])
    uniq, inv = np.unique(pids, return_inverse=True)
    C = np.zeros((len(uniq), E.shape[1]), np.float32)
    np.add.at(C, inv, E)
    return uniq, C / np.linalg.norm(C, axis=1, keepdims=True)


def person_thumbs(pid, n=4):
    return [square(t, 96) for (t,) in con.execute(
        "SELECT thumb FROM faces WHERE person_id = ? AND thumb IS NOT NULL ORDER BY size * det_score DESC LIMIT ?",
        (pid, n))]


def similar_page():
    url_page("similar")
    st.subheader("Похожие люди")
    st.caption("Пары кластеров с похожими «средними лицами»: часто это один человек, разбитый на два "
               "(разные годы, очки, борода). Бывают и родственники — для них «Это разные люди».")
    url_init("sim_thr", "min", 0.45, float)
    url_init("sim_files", "files", 3, int)
    c1, c2 = st.columns(2)
    thr = c1.slider("Минимальная похожесть", 0.30, 0.80, step=0.01, key="sim_thr")
    min_files = c2.number_input("Мин. файлов у каждого", 1, 1000, key="sim_files")
    url_set("min", thr, 0.45)
    url_set("files", min_files, 3)

    sig = con.execute("SELECT COUNT(*), COALESCE(SUM(person_id), 0) FROM faces WHERE person_id IS NOT NULL").fetchone()
    pids, C = person_centroids(sig)
    info = {pid: (name, nf, hidden) for pid, name, nf, hidden in con.execute(
        "SELECT id, name, n_files, hidden FROM persons WHERE n_faces > 0")}
    keep = np.array([p in info and info[p][1] >= min_files and not info[p][2] for p in pids])
    pids, C = pids[keep], C[keep]
    not_same = set(con.execute("SELECT a, b FROM not_same"))
    S = C @ C.T
    a, b = np.where(np.triu(S >= thr, k=1))
    pairs = sorted(((float(S[i, j]), int(pids[i]), int(pids[j])) for i, j in zip(a, b)), reverse=True)
    pairs = [p for p in pairs if (min(p[1], p[2]), max(p[1], p[2])) not in not_same]
    st.caption(f"Найдено пар: {len(pairs)}")

    key = f"similar_{thr}_{min_files}"
    for sim, p1, p2 in pairs[:shown(key)]:
        if info[p1][1] < info[p2][1]:  # bigger cluster first: it keeps its id on merge
            p1, p2 = p2, p1
        with st.container(border=True, key=f"mount_sim_{p1}_{p2}"):
            cols = st.columns([4, 1, 4], vertical_alignment="center")
            for col, pid in ((cols[0], p1), (cols[2], p2)):
                name, nf, _ = info[pid]
                col.page_link(PAGES["person"], label=f"{label(pid, name)} · {nf} файлов", query_params={"id": pid})
                col.image(person_thumbs(pid), width=96)
            cols[1].metric("похожесть", f"{sim:.2f}")
            if cols[1].button("Объединить", key=f"m_{p1}_{p2}", type="primary"):
                ps.merge_persons(con, [p1, p2])
                st.cache_data.clear()
                st.rerun()
            if cols[1].button("Это разные люди", key=f"d_{p1}_{p2}"):
                con.execute("INSERT OR IGNORE INTO not_same (a, b) VALUES (?, ?)", (min(p1, p2), max(p1, p2)))
                con.commit()
                st.rerun()
    load_more(key, len(pairs))
    url_done("similar")


# ---------------------------------------------------------------- collections: gather -> zip -> send -> delete

def open_in_explorer(path):
    os.makedirs(path, exist_ok=True)
    # runs on the server: no file manager to open, show where the folder is on the NAS
    st.toast(f"Папка: {ps.map_path(path, to_windows=True)}")


@st.dialog("Отправлено — удалить подборку?", width="large")
def confirm_finish(cid, name, n, size_mb):
    st.write(f"Файлы подборки **«{name}»** ({n} шт., {size_mb:.0f} МБ) будут перемещены в карантин {ps.QUARANTINE_DIR}, "
             "ZIP-архив тоже. Подборка закроется.")
    st.caption("Из карантина их ещё можно вернуть (`photosorter.py restore`), пока папку не очистили.")
    keep_zip = st.checkbox("ZIP-архив оставить")
    sure = st.checkbox(f"Да, я отправил(а) и удаляю эти {n} файлов")
    if st.button("Удалить", type="primary", disabled=not sure):
        done, failed = ps.finish_collection(con, cid, delete_zip=not keep_zip)
        state.flash = f"«{name}»: удалено {done}" + (f" · ⚠ не удалось: {len(failed)}" if failed else "")
        st.rerun()


def collections_page():
    url_page("collections")
    st.subheader("Подборки")
    st.caption("Собери файлы для отправки (кнопка «📥 В …» в любом списке или в просмотре), упакуй в ZIP, "
               "отправь — и удали. Файлы при сборе **перемещаются** из архива в папку подборки на этом компьютере.")
    root = ps.collections_root(con)
    c1, c2 = st.columns([4, 1], vertical_alignment="bottom")
    new_root = c1.text_input("Где хранить подборки", value=root)
    if new_root != root:
        ps.set_setting(con, "collections_root", new_root)
        st.rerun()
    if c2.button("📂 Открыть", key="open_root"):
        open_in_explorer(root)
    if msg := state.pop("flash", None):
        st.success(msg)

    colls = con.execute("""
        SELECT k.id, k.name, k.folder, COUNT(c.file_id), COALESCE(SUM(f.size), 0) / 1e6,
               SUM(f.kind = 'video')
        FROM collections k LEFT JOIN collection_files c ON c.collection_id = k.id
        LEFT JOIN files f ON f.id = c.file_id GROUP BY k.id ORDER BY k.created_at DESC
    """).fetchall()
    if not colls:
        st.info("Подборок пока нет. Создай её в боковой панели или просто нажми «📥» у выделенных файлов.")
        url_done("collections")
        return

    url_init("coll_open", "c", colls[0][0], int)
    if state.coll_open not in [c[0] for c in colls]:
        state.coll_open = colls[0][0]
    for cid, name, folder, n, mb, n_video in colls:
        with st.container(border=True, key=f"mount_c_{cid}"):
            z = ps.collection_zip_path(con, cid)
            zinfo = f" · 📦 ZIP {os.path.getsize(z) / 1e6:.0f} МБ" if os.path.exists(z) else ""
            cols = st.columns([4, 1, 1, 1, 1.4], vertical_alignment="center")
            cols[0].markdown(f"**{name}** · {n} файлов ({n_video or 0} видео) · {mb:.0f} МБ{zinfo}  \n"
                             f"<small>{folder}</small>", unsafe_allow_html=True)
            if cols[1].button("👁 Показать", key=f"show_{cid}", disabled=state.coll_open == cid):
                state.coll_open = cid
                st.rerun()
            if cols[2].button("📂 Папка", key=f"open_{cid}"):
                open_in_explorer(folder)
            if cols[3].button("📦 ZIP", key=f"zip_{cid}", disabled=not n,
                              help="Собрать архив рядом с папкой и открыть её — останется перетащить в мессенджер"):
                with st.spinner(f"Пакую «{name}»..."):
                    out, k = ps.zip_collection(con, cid)
                open_in_explorer(os.path.dirname(out))
                state.flash = f"Готово: {out} ({k} файлов)"
                st.rerun()
            if cols[4].button("✅ Отправлено — удалить", key=f"done_{cid}", disabled=not n, type="primary"):
                confirm_finish(cid, name, n, mb)
            if not n and cols[4].button("Удалить пустую", key=f"rm_{cid}"):
                con.execute("DELETE FROM collections WHERE id = ?", (cid,))
                con.commit()
                st.rerun()
    url_set("c", state.coll_open, colls[0][0])

    cid = state.coll_open
    name = next(c[1] for c in colls if c[0] == cid)
    st.markdown(f"#### Содержимое «{name}»")
    rows = con.execute("""
        SELECT f.id, f.path, f.kind, f.taken_at, f.place, c.original_path,
               (SELECT group_concat(k.name, ', ') FROM collection_files o JOIN collections k ON k.id = o.collection_id
                WHERE o.file_id = f.id AND o.collection_id != c.collection_id)
        FROM collection_files c JOIN files f ON f.id = c.file_id
        WHERE c.collection_id = ? ORDER BY f.taken_at, f.path
    """, (cid,)).fetchall()
    items = []
    for fid, path, kind, taken_at, place, original, also in rows:
        it = preview_row(fid, path, kind, taken_at, place,
                         (f"\n📥 ещё в: {also}" if also else "") + f"\n↩ {os.path.dirname(original)}", coll=False)
        items.append(it)
    st.caption("«↩ Убрать из подборки»: если файл есть и в других подборках, он останется там; "
               "если только здесь — вернётся в архив на прежнее место.")
    act = file_grid(f"coll_{cid}", items, videos=True, collect=False, extra_actions=[
        {"name": "uncollect", "label": "↩ Убрать из подборки", "label_sel": "↩ Убрать из подборки ({n})",
         "need_sel": True}])
    if act and act["name"] == "uncollect" and act["ids"]:
        done, failed = ps.return_from_collection(con, [(cid, fid) for fid in act["ids"]])
        state.flash = f"Убрано: {done}" + "".join(f" · ⚠ {w}" for _, w in failed)
        st.rerun()
    url_done("collections")


# ---------------------------------------------------------------- navigation: every section has its own URL

PAGES = {
    "people": st.Page(people_page, title="Люди", icon="👥", url_path="people", default=True),
    "person": st.Page(person_page, title="Человек", url_path="person", visibility="hidden"),
    "group": st.Page(group_page, title="Только выбранные люди", icon="🎯", url_path="only"),
    "similar": st.Page(similar_page, title="Похожие люди", icon="🔗", url_path="similar"),
    "search": st.Page(search_page, title="Поиск", icon="🔎", url_path="search"),
    "files": st.Page(all_files_page, title="Все файлы", icon="🗂", url_path="files"),
    "nopeople": st.Page(no_people_page, title="Без людей", icon="🏞", url_path="no-people"),
    "strangers": st.Page(strangers_page, title="Только посторонние", icon="👤", url_path="strangers"),
    "videos": st.Page(videos_page, title="Видео", icon="🎬", url_path="videos"),
    "stacks": st.Page(stacks_page, title="Стопки похожих", icon="📚", url_path="stacks"),
    "copies": st.Page(copies_page, title="Копии", icon="👯", url_path="copies"),
    "collections": st.Page(collections_page, title="Подборки", icon="📥", url_path="collections"),
}
nav = st.navigation(list(PAGES.values()))
IN_COLLECTION = in_collection()
PEOPLE_IN = people_in()

with st.sidebar:
    n_files, n_faces = con.execute("SELECT COUNT(*), COALESCE(SUM(n_faces), 0) FROM files").fetchone()
    n_people = con.execute("SELECT COUNT(*) FROM persons WHERE n_faces > 0").fetchone()[0]
    st.caption(f"{n_files} файлов · {n_faces} лиц · {n_people} людей")
    st.toggle("Показывать и видео", key="show_video",
              help="Выключено: в списках только фото, чтобы массовое удаление не задело видео. "
                   "Видео отдельно — в разделе «Видео».")
    if "hide_collected" not in state:
        state.hide_collected = True
    st.toggle("Скрывать то, что в подборках", key="hide_collected",
              help="Фото, уже разложенные по подборкам, не показываются в списках. "
                   "Их видно на странице «Подборки».")

    # current collection: where "📥" moves the selected files
    colls = dict(con.execute("SELECT id, name FROM collections ORDER BY created_at DESC"))
    if "_next_collection" in state:
        state.collection = state.pop("_next_collection")
    if state.get("collection") not in colls:
        state.collection = next(iter(colls), None)
    c1, c2 = st.columns([3, 1], vertical_alignment="bottom")
    c1.selectbox("📥 Подборка", list(colls) or [None], key="collection",
                 format_func=lambda c: colls.get(c, "— нет —"), help="Куда кнопка «📥» кладёт выделенные файлы")
    st.radio("В подборку", ["Перемещать", "Копировать"], key="collect_mode", horizontal=True,
             help="Перемещать — файл уезжает из архива в папку подборки. "
                  "Копировать — в подборку кладётся копия, оригинал остаётся в архиве.")

    def _create_collection():
        name = state.new_coll_name.strip()
        if name and name not in colls.values():
            state.collection = ps.create_collection(con, name)
        state.new_coll_name = ""

    with c2.popover("➕", help="Новая подборка"):
        st.text_input("Название", placeholder="Для мамы, Саше с моря...", key="new_coll_name")
        st.button("Создать", on_click=_create_collection)
    if st.button("Пересчитать кластеры", help="Имена и слияния сохраняются"):
        with st.spinner("Кластеризация..."):
            ps.cmd_cluster(ps.build_parser().parse_args(["--db", DB, "cluster"]))
        st.cache_data.clear()
        st.rerun()
    st.caption(f"База: {DB}")

nav.run()
