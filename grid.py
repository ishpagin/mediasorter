"""Explorer-like photo grid for Streamlit (components v2).

Selection lives entirely in the browser (no Python reruns while clicking):
click = select one, Ctrl/Cmd+click = toggle, Shift+click = range, drag on the grid = rubber band,
Ctrl+A = select all loaded, Esc = clear, Delete / Backspace = delete selected, the bin on hover = delete one.
Double click / Enter / ⤢ = full-screen viewer (originals via media_server): ←/→, Delete, F = fullscreen, Esc,
Ctrl+D = delete at once (no dialog), Ctrl+Z = undo the last deletion (also in the grid), Tab / Shift+Tab = next /
previous stack.
Python only receives actions: {"name": ..., "ids": [...]}.

Each image is sent to the browser once per session and cached there (window.__psImg),
so loading more tiles only transfers the new previews.
"""
import base64

import streamlit as st

_JS = r"""
// ':material/name:' (Streamlit's own label syntax) -> an icon from the Material Symbols font Streamlit already ships
const esc = (t) => String(t).replace(/[&<>"]/g, (c) => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c]));
const ico = (name) => `<span class="ps-ico" aria-hidden="true">${name}</span>`;
const rich = (t) => esc(t).replace(/:material\/([a-z0-9_]+):/g, (_, n) => ico(n));
const plain = (t) => String(t).replace(/:material\/[a-z0-9_]+:\s?/g, '');

export default function(component) {
    const { data, parentElement, setTriggerValue } = component;
    const cache = (window.__psImg = window.__psImg || new Map());

    let root = parentElement.querySelector('.ps-root');
    if (!root) {
        root = document.createElement('div');
        root.className = 'ps-root';
        root.tabIndex = 0;
        root.innerHTML = `
            <div class="ps-bar"><span class="ps-count"></span><span class="ps-actions"></span>
              <span class="ps-hint">клик — выбрать · Ctrl/Shift+клик — несколько · рамка мышью ·
              Ctrl+A — все загруженные · двойной клик / Enter — открыть · Delete / Backspace — удалить · Ctrl+Z — вернуть ·
              Esc — снять</span></div>
            <div class="ps-grid"></div>
            <div class="ps-sentinel"></div>
            <div class="ps-band"></div>
            <div class="ps-view" hidden>
              <img class="ps-vimg" alt=""><video class="ps-vvid" controls playsinline hidden></video>
              <div class="ps-vtop"><span class="ps-vcap"></span>
                <span class="ps-vhint">← → листать · Tab / Shift+Tab стопки · Delete удалить · Ctrl+Z вернуть · F экран · Esc</span>
                <span class="ps-vbtns"><span class="ps-vacts"></span>
                <button class="ps-vbtn ps-vfs" title="Во весь экран (F)">${ico('fullscreen')}</button>
                <button class="ps-vbtn danger ps-vdel" title="Удалить (Delete)">${ico('delete')} Удалить</button>
                <button class="ps-vbtn ps-vclose" title="Закрыть (Esc)">${ico('close')}</button></span></div>
              <button class="ps-vnav ps-vprev" title="Предыдущее (←)">${ico('chevron_left')}</button>
              <button class="ps-vnav ps-vnext" title="Следующее (→)">${ico('chevron_right')}</button>
            </div>`;
        parentElement.appendChild(root);
        root._s = { list: null, sel: new Set(), anchor: null, order: [], tiles: new Map(), heads: new Map(), items: new Map(),
                    hover: false, viewing: null, reopen: null };
        setupEvents(root);
        setupViewer(root);
    }
    const s = root._s;
    root._api = { setTriggerValue, data };
    const grid = root.querySelector('.ps-grid');
    // the action bar sticks below Streamlit's fixed header, not under it
    const header = document.querySelector('header[data-testid="stHeader"]');
    root.style.setProperty('--ps-top', (header ? Math.round(header.getBoundingClientRect().height) : 0) + 'px');

    if (s.list !== data.list) {  // another list (filters changed): start over
        grid.innerHTML = '';
        s.tiles = new Map();
        s.heads = new Map();
        s.sel.clear();
        s.anchor = null;
        s.list = data.list;
    }

    // sync tiles with data.items, keeping existing DOM nodes (no flicker, scroll position kept)
    const ids = data.items.map((it) => it.id);
    const present = new Set(ids);
    for (const [id, el] of s.tiles) {
        if (!present.has(id)) { el.remove(); s.tiles.delete(id); s.sel.delete(id); }
    }
    // group headers (it.head: a full-width line before the item, e.g. a stack of similar photos)
    const headed = new Set(data.items.filter((it) => it.head).map((it) => it.id));
    for (const [id, el] of s.heads) if (!headed.has(id)) { el.remove(); s.heads.delete(id); }
    const missing = [];
    let prev = null, fresh = 0;
    for (const it of data.items) {
        if (it.src) cache.set(it.img, it.src);
        let el = s.tiles.get(it.id);
        if (!el) {
            el = document.createElement('div');
            el.className = 'ps-tile';
            el.dataset.id = it.id;
            el.style.setProperty('--i', fresh++);  // new tiles lay out one after another, like a contact sheet
            el.innerHTML = `<div class="ps-img"><img draggable="false" alt="">
                <button class="ps-open" title="Открыть на весь экран (двойной клик)">${ico('open_in_full')}</button>
                <button class="ps-del" title="Удалить этот файл">${ico('delete')}</button><span class="ps-check">${ico('check')}</span></div>
                <div class="ps-cap"></div>`;
            s.tiles.set(it.id, el);
        }
        if (it.head) {
            let h = s.heads.get(it.id);
            if (!h) { h = document.createElement('div'); h.className = 'ps-head'; s.heads.set(it.id, h); }
            h.innerHTML = rich(it.head);
            const wantH = prev ? prev.nextSibling : grid.firstChild;
            if (wantH !== h) grid.insertBefore(h, wantH);
            prev = h;
        }
        const img = el.querySelector('img');
        const src = cache.get(it.img);
        if (src) { if (img.getAttribute('src') !== src) img.setAttribute('src', src); }
        else missing.push(it.img);
        el.style.display = '';  // may have been hidden by a quick delete that was undone
        el.querySelector('.ps-cap').innerHTML = it.cap.split('\n').filter(Boolean)
            .map((l) => `<div class="ps-line">${rich(l)}</div>`).join('');  // one line each, full text in the tooltip
        el.title = plain(it.cap + '\n' + (it.title || '')).trim();
        const want = prev ? prev.nextSibling : grid.firstChild;
        if (want !== el) grid.insertBefore(el, want);
        prev = el;
    }
    s.order = ids;
    s.items = new Map(data.items.map((it) => [it.id, it]));
    paint(root);

    // quick deletes (Ctrl+D) are resent until the file disappears from the list
    const pending = (window.__psPending = window.__psPending || new Set());
    for (const id of [...pending]) if (!present.has(id)) pending.delete(id);
    for (const id of pending) { const el = s.tiles.get(id); if (el) el.style.display = 'none'; }

    // after Ctrl+Z in the viewer: show the file that came back
    const show = window.__psShow;
    if (show && show.list === data.list && present.has(show.id)) {
        window.__psShow = null;
        openView(root, show.id);
    }

    // after deleting from the viewer: once the file is gone, continue with the next one
    // (kept on window: the grid may be remounted by the rerun that follows the deletion)
    const r = window.__psReopen;
    if (r && r.list === data.list) {
        if (Date.now() > r.until) window.__psReopen = null;
        else if (!present.has(r.deleted)) {
            window.__psReopen = null;
            if (present.has(r.next)) openView(root, r.next);
        }
    }

    // the browser lost some cached images (e.g. an interrupted run): ask Python to resend them
    if (missing.length) setTriggerValue('need', missing);

    // infinite scroll
    if (root._obs) root._obs.disconnect();
    if (data.has_more) {
        let fired = false;
        root._obs = new IntersectionObserver((entries) => {
            if (!fired && entries.some((e) => e.isIntersecting)) {
                fired = true;
                setTriggerValue('more', ids.length);
            }
        }, { rootMargin: '1200px' });
        root._obs.observe(root.querySelector('.ps-sentinel'));
    }
    return () => { if (root._obs) root._obs.disconnect(); };
}

function fire(root, name, ids) {
    root._api.setTriggerValue('action', { name, ids, nonce: Date.now() });
}

// ---------------------------------------------------------------- full-screen viewer

function fileUrl(root, id) {
    const d = root._api.data;
    return `${d.media}/file?id=${id}&t=${d.token}`;
}

// thumbnails of the opened photo's group (a stack / copies), from the previews the grid already has
function viewStrip(root, v, it, id) {
    const s = root._s;
    let strip = v.querySelector('.ps-vstrip');
    if (!strip) {
        strip = document.createElement('div');
        strip.className = 'ps-vstrip';
        strip.addEventListener('click', (e) => {
            const t = e.target.closest('.ps-vthumb');
            if (t) openView(root, Number(t.dataset.id));
        });
        v.appendChild(strip);
    }
    const ids = it.group == null ? [] : s.order.filter((x) => s.items.get(x)?.group === it.group);
    const show = ids.length > 1;
    strip.hidden = !show;
    v.classList.toggle('strip', show);
    if (!show) { strip.dataset.ids = ''; return; }
    const key = ids.join(',');
    if (strip.dataset.ids !== key) {  // another group (or it changed): rebuild
        strip.dataset.ids = key;
        strip.innerHTML = ids.map((x) => {
            const n = s.items.get(x);
            const src = (window.__psImg && window.__psImg.get(n.img)) || '';
            return `<button class="ps-vthumb" data-id="${x}" title="${esc(plain(n.cap.split('\n')[0]))}">`
                 + (src ? `<img src="${src}" alt="">` : '') + '</button>';
        }).join('');
    }
    for (const b of strip.children) b.classList.toggle('cur', Number(b.dataset.id) === id);
    const cur = strip.querySelector('.cur');
    if (cur) cur.scrollIntoView({ block: 'nearest', inline: 'center' });
}

function openView(root, id) {
    const s = root._s;
    const it = s.items.get(id);
    if (!it || !root._api.data.media) return;
    const v = root.querySelector('.ps-view');
    const img = v.querySelector('.ps-vimg'), vid = v.querySelector('.ps-vvid');
    s.viewing = id;
    v.hidden = false;
    if (it.kind === 'video') {
        img.hidden = true;
        vid.hidden = false;
        vid.src = fileUrl(root, id);
        vid.play().catch(() => {});
    } else {
        stopVideo(vid);
        img.hidden = false;
        img.src = (window.__psImg && window.__psImg.get(it.img)) || '';  // instant low-res placeholder
        const full = new Image();
        full.onload = () => { if (s.viewing === id) img.src = full.src; };
        full.onerror = () => {
            if (s.viewing === id) v.querySelector('.ps-vcap').innerHTML += rich(' · :material/warning: оригинал недоступен (удалён или нет доступа к NAS)');
        };
        full.src = fileUrl(root, id);
    }
    vid.onerror = () => {
        if (s.viewing === id) v.querySelector('.ps-vcap').innerHTML += rich(' · :material/warning: браузер не может воспроизвести это видео');
    };
    const i = s.order.indexOf(id);
    v.querySelector('.ps-vcap').innerHTML = rich(`${i + 1} / ${root._api.data.total} · ` + it.cap.replace(/\n/g, ' · '));
    viewStrip(root, v, it, id);
    for (const k of [1, -1]) {  // preload neighbours
        const n = s.items.get(s.order[i + k]);
        if (n && n.kind !== 'video') new Image().src = fileUrl(root, n.id);
    }
    if (i >= s.order.length - 3 && root._api.data.has_more) root._api.setTriggerValue('more', s.order.length);
    s.sel = new Set([id]);
    s.anchor = id;
    paint(root);
    const tile = s.tiles.get(id);
    if (tile) tile.scrollIntoView({ block: 'nearest' });
}

function stopVideo(vid) {
    vid.pause();
    vid.removeAttribute('src');
    vid.load();
    vid.hidden = true;
}

function closeView(root) {
    const v = root.querySelector('.ps-view');
    v.hidden = true;
    stopVideo(v.querySelector('.ps-vvid'));
    root._s.viewing = null;
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
}

function stepView(root, k) {
    const s = root._s;
    const next = s.order[s.order.indexOf(s.viewing) + k];
    if (next !== undefined) openView(root, next);
}

// Tab / Shift+Tab: first photo of the next / previous group (stack, copies); no groups here: next / previous photo
function jumpGroup(root, k) {
    const s = root._s, cur = s.items.get(s.viewing);
    if (!cur || cur.group == null) { stepView(root, k); return; }
    const grp = (x) => s.items.get(x)?.group;
    let i = s.order.indexOf(s.viewing);
    while (s.order[i] !== undefined && grp(s.order[i]) === cur.group) i += k;  // leave this group
    if (s.order[i] === undefined) {  // past the loaded part: ask for more, Tab again when it is there
        if (k > 0 && root._api.data.has_more) root._api.setTriggerValue('more', s.order.length);
        return;
    }
    if (k < 0) while (i > 0 && grp(s.order[i - 1]) === grp(s.order[i])) i--;  // start of the previous group
    openView(root, s.order[i]);
}

function deleteViewed(root) {
    const s = root._s;
    const id = s.viewing;
    if (id === null) return;
    const i = s.order.indexOf(id);
    const next = s.order[i + 1] !== undefined ? s.order[i + 1] : s.order[i - 1];
    window.__psReopen = { list: root._api.data.list, deleted: id, next, until: Date.now() + 120000 };
    closeView(root);
    fire(root, 'delete', [id]);
}

// Ctrl+D: delete right away (no dialog) and move on; Ctrl+Z brings it back
function quickDeleteViewed(root) {
    const s = root._s;
    const id = s.viewing;
    if (id === null) return;
    const i = s.order.indexOf(id);
    const next = s.order[i + 1] !== undefined ? s.order[i + 1] : s.order[i - 1];
    const pending = (window.__psPending = window.__psPending || new Set());
    pending.add(id);
    (window.__psUndoIds = window.__psUndoIds || []).push(id);
    const tile = s.tiles.get(id);
    if (tile) tile.style.display = 'none';
    s.order = s.order.filter((x) => x !== id);  // until Python confirms, navigation skips it
    if (next !== undefined) openView(root, next); else closeView(root);
    fire(root, 'quick_delete', [...pending]);  // all pending: fast repeats must not get lost
}

function undoDelete(root) {
    const id = (window.__psUndoIds || []).pop();
    if (id !== undefined) {
        if (window.__psPending) window.__psPending.delete(id);
        if (root._s.viewing !== null) window.__psShow = { list: root._api.data.list, id };
    }
    fire(root, 'undo', []);
}

function toggleFullscreen(root) {
    const v = root.querySelector('.ps-view');
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else v.requestFullscreen().catch(() => {});
}

function setupViewer(root) {
    const v = root.querySelector('.ps-view');
    v.addEventListener('click', (e) => { if (e.target === v) closeView(root); });
    v.querySelector('.ps-vclose').onclick = () => closeView(root);
    v.querySelector('.ps-vprev').onclick = () => stepView(root, -1);
    v.querySelector('.ps-vnext').onclick = () => stepView(root, 1);
    v.querySelector('.ps-vdel').onclick = () => deleteViewed(root);
    v.querySelector('.ps-vfs').onclick = () => toggleFullscreen(root);
}

function hideWhileDialog(root) {
    // a Streamlit dialog opens under the viewer (and the viewer lives in a shadow root): step aside until it closes
    const v = root.querySelector('.ps-view');
    let seen = false, waited = 0;
    const t = setInterval(() => {
        const open = !!document.querySelector('[role="dialog"]');
        seen = seen || open;
        waited += 100;
        if (open) v.style.visibility = 'hidden';
        else if (seen || waited > 10000) { v.style.visibility = ''; clearInterval(t); }
    }, 100);
}

function paintViewerActions(root) {
    // actions that make sense for the file on screen (e.g. "to collection"): apply, then show the next file
    const box = root.querySelector('.ps-vacts');
    box.innerHTML = '';
    for (const a of root._api.data.actions.filter((x) => x.viewer)) {
        const b = document.createElement('button');
        b.className = 'ps-vbtn';
        b.innerHTML = rich(a.viewer);
        b.onclick = () => {
            const s = root._s, id = s.viewing;
            if (id === null) return;
            if (!a.stay) stepView(root, 1);  // stay: the action asks about this very file (a dialog)
            else {
                hideWhileDialog(root);
                // if the dialog makes the file leave this list (moved to a collection...), go on with the next one
                const i = s.order.indexOf(id);
                const next = s.order[i + 1] !== undefined ? s.order[i + 1] : s.order[i - 1];
                window.__psReopen = { list: root._api.data.list, deleted: id, next, until: Date.now() + 120000 };
            }
            fire(root, a.name, [id]);
        };
        box.appendChild(b);
    }
}

function paint(root) {
    const s = root._s;
    const data = root._api.data;
    paintViewerActions(root);
    for (const [id, el] of s.tiles) el.classList.toggle('sel', s.sel.has(id));
    const n = s.sel.size;
    root.querySelector('.ps-count').textContent =
        `Загружено ${s.order.length} из ${data.total}` + (n ? ` · выбрано ${n}` : '');
    const box = root.querySelector('.ps-actions');
    box.innerHTML = '';
    for (const a of data.actions) {
        const b = document.createElement('button');
        b.className = 'ps-btn' + (a.danger ? ' danger' : '');
        b.innerHTML = rich(n && a.label_sel ? a.label_sel.replace('{n}', n) : a.label);
        b.disabled = (a.need_sel && !n) || (a.no_sel && n > 0 && !a.label_sel);
        b.onclick = (e) => { e.stopPropagation(); fire(root, a.name, [...s.sel]); };
        box.appendChild(b);
    }
}

function setupEvents(root) {
    const s = root._s;
    const grid = root.querySelector('.ps-grid');
    const band = root.querySelector('.ps-band');
    const idOf = (el) => Number(el.dataset.id);

    root.addEventListener('mouseenter', () => { s.hover = true; });
    root.addEventListener('mouseleave', () => { s.hover = false; });

    grid.addEventListener('dblclick', (e) => {
        const tile = e.target.closest('.ps-tile');
        if (tile && !e.target.closest('.ps-del')) openView(root, idOf(tile));
    });

    grid.addEventListener('click', (e) => {
        if (s.justDragged) return;
        const tile = e.target.closest('.ps-tile');
        if (!tile) { if (!e.ctrlKey && !e.metaKey) { s.sel.clear(); paint(root); } return; }
        const id = idOf(tile);
        if (e.target.closest('.ps-del')) { fire(root, 'delete', [id]); return; }
        if (e.target.closest('.ps-open')) { openView(root, id); return; }
        const a = e.shiftKey && s.anchor !== null ? s.order.indexOf(s.anchor) : -1;
        if (a >= 0) {  // shift: add the range from the last picked tile, keep what is selected already
            const b = s.order.indexOf(id);
            for (let i = Math.min(a, b); i <= Math.max(a, b); i++) s.sel.add(s.order[i]);
            s.anchor = id;
        } else if (e.shiftKey || e.ctrlKey || e.metaKey) {  // shift without a usable anchor: just this one, never from the start
            s.sel.has(id) ? s.sel.delete(id) : s.sel.add(id);
            s.anchor = id;
        } else {
            s.sel.clear(); s.sel.add(id); s.anchor = id;
        }
        root.focus({ preventScroll: true });
        paint(root);
    });

    // nearest scrollable ancestor (crosses the shadow root boundary)
    const scroller = () => {
        let el = root.getRootNode().host;
        while (el && el !== document.body) {
            const oy = getComputedStyle(el).overflowY;
            if ((oy === 'auto' || oy === 'scroll') && el.scrollHeight > el.clientHeight) return el;
            el = el.parentElement || (el.getRootNode() && el.getRootNode().host);
        }
        return document.scrollingElement;
    };
    const NO_START = 'input, textarea, select, button, a, label, [role="button"], [role="option"], [role="listbox"], ' +
                     '[role="dialog"], [role="slider"], [contenteditable="true"], [data-testid="stSidebar"]';

    // rubber band selection: may start anywhere in empty space of the main area, survives scrolling
    const onDown = (e) => {
        if (!root.isConnected) { document.removeEventListener('mousedown', onDown); return; }
        if (e.button !== 0) return;
        const path = e.composedPath().filter((n) => n instanceof Element);
        if (root._s.viewing !== null) return;
        if (path.some((n) => ['ps-del', 'ps-open', 'ps-bar', 'ps-view'].some((c) => n.classList.contains(c)))) return;
        const inGrid = path.includes(grid);
        if (!inGrid) {
            if (path.some((n) => n.matches(NO_START))) return;
            if (!path.some((n) => n.matches('[data-testid="stMain"], [data-testid="stAppViewContainer"]'))) return;
            e.preventDefault();  // no text selection while dragging over the page
        }
        const g0 = grid.getBoundingClientRect();
        const ax = e.clientX - g0.left, ay = e.clientY - g0.top;  // anchor relative to the grid: stable on scroll
        const base = (e.ctrlKey || e.metaKey) ? new Set(s.sel) : new Set();
        const sc = scroller();
        let mx = e.clientX, my = e.clientY, dragging = false, raf = 0;

        const update = () => {
            const g = grid.getBoundingClientRect();
            const x0 = g.left + ax, y0 = g.top + ay;
            if (!dragging && Math.hypot(mx - x0, my - y0) < 6) return;
            dragging = true;
            const l = Math.min(x0, mx), t = Math.min(y0, my), r = Math.max(x0, mx), b = Math.max(y0, my);
            Object.assign(band.style, { display: 'block', left: l + 'px', top: t + 'px',
                                        width: (r - l) + 'px', height: (b - t) + 'px' });
            s.sel = new Set(base);
            let last = -1;  // the band's end tile (in the drag direction) becomes the anchor for Shift+click
            for (const [id, el] of s.tiles) {
                const q = el.getBoundingClientRect();
                if (q.right > l && q.left < r && q.bottom > t && q.top < b) {
                    s.sel.add(id);
                    const i = s.order.indexOf(id);
                    if (last < 0 || (my >= y0 ? i > last : i < last)) last = i;
                }
            }
            if (last >= 0) s.anchor = s.order[last];
            paint(root);
        };
        // auto-scroll while the pointer is near the top/bottom edge of the window
        const tick = () => {
            const edge = 60, h = window.innerHeight;
            const dy = my < edge ? -(edge - my) : my > h - edge ? (my - (h - edge)) : 0;
            if (dragging && dy) { sc.scrollBy(0, dy * 0.5); update(); }
            raf = requestAnimationFrame(tick);
        };
        const move = (ev) => { mx = ev.clientX; my = ev.clientY; update(); };
        const onScroll = () => update();
        const up = () => {
            document.removeEventListener('mousemove', move);
            document.removeEventListener('mouseup', up);
            document.removeEventListener('scroll', onScroll, true);
            cancelAnimationFrame(raf);
            band.style.display = 'none';
            if (dragging) {
                s.justDragged = true;  // swallow the click that follows this mouseup
                setTimeout(() => { s.justDragged = false; }, 0);
            } else if (!inGrid && !(e.ctrlKey || e.metaKey) && s.sel.size) {
                s.sel.clear();  // plain click on empty space clears the selection, like Explorer
                paint(root);
            }
        };
        document.addEventListener('mousemove', move);
        document.addEventListener('mouseup', up);
        document.addEventListener('scroll', onScroll, true);
        raf = requestAnimationFrame(tick);
    };
    document.addEventListener('mousedown', onDown);

    const onKey = (e) => {
        if (!root.isConnected) { document.removeEventListener('keydown', onKey); window.__psRoots.delete(root); return; }
        const t = e.composedPath()[0];
        // typing in a text field is not for us; a focused switch/checkbox (just clicked) is
        if (t && ((t.tagName === 'INPUT' && !['checkbox', 'radio'].includes(t.type))
                  || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
        if (document.querySelector('[role="dialog"]')) return;
        const ctrl = e.ctrlKey || e.metaKey;  // e.code: physical key, works on any keyboard layout
        if (s.viewing !== null) {  // full-screen viewer has the keyboard
            let fn = null;
            if (ctrl && e.code === 'KeyD') fn = () => quickDeleteViewed(root);
            else if (ctrl && e.code === 'KeyZ') fn = () => undoDelete(root);
            else if (!ctrl) fn = { Escape: () => closeView(root), ArrowRight: () => stepView(root, 1),
                                   ArrowLeft: () => stepView(root, -1), Delete: () => deleteViewed(root),
                                   Backspace: () => deleteViewed(root), Tab: () => jumpGroup(root, e.shiftKey ? -1 : 1),
                                   KeyF: () => toggleFullscreen(root) }[e.code === 'KeyF' ? 'KeyF' : e.key];
            if (fn) { e.preventDefault(); fn(); }
            return;
        }
        if (ctrl && e.code === 'KeyZ') {  // anywhere on the page; one grid answers: the hovered / focused one, else the first
            const live = [...window.__psRoots].filter((r) => r.isConnected);
            const owner = live.find((r) => r._s.hover || r.matches(':focus-within')) || live[0];
            if (owner === root) { e.preventDefault(); undoDelete(root); }
            return;
        }
        if (!s.hover && !root.matches(':focus-within')) return;
        if ((e.key === 'Enter' || e.key === ' ') && s.sel.size) {
            e.preventDefault();
            openView(root, s.sel.has(s.anchor) ? s.anchor : [...s.sel][0]);
        } else if (ctrl && e.code === 'KeyA') {
            e.preventDefault(); s.sel = new Set(s.order); paint(root);
        } else if (e.key === 'Escape') {
            s.sel.clear(); paint(root);
        } else if ((e.key === 'Delete' || e.key === 'Backspace') && s.sel.size) {
            e.preventDefault(); fire(root, 'delete', [...s.sel]);
        }
    };
    (window.__psRoots ||= new Set()).add(root);
    document.addEventListener('keydown', onKey);
}
"""

_CSS = r"""
/* Light table: photos sit in slide mounts on a lit surface; grease pencil yellow marks selects, red marks rejects */
.ps-root {
    outline: none; font-family: inherit;
    --lt-surface: #E6EAEE; --lt-mount: #FBFBF8; --lt-edge: #CDD3DA; --lt-ink: #1D2024; --lt-soft: #5E656E;
    --lt-frame: #24272B; --lt-pick: #F2B705; --lt-reject: #C2361F;
    --lt-ease: cubic-bezier(0.2, 0.8, 0.2, 1);
}
.ps-bar {
    position: sticky; top: var(--ps-top, 0px); z-index: 5; display: flex; flex-wrap: wrap; gap: 8px; align-items: center;
    padding: 12px 0; background: var(--lt-surface); font-size: 14px; color: var(--lt-ink);
}
.ps-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.ps-hint { font-size: 12px; color: var(--lt-soft); }
.ps-btn {
    font: inherit; font-size: 14px; font-weight: 500; padding: 6px 12px; border-radius: 6px; cursor: pointer;
    border: 1px solid var(--lt-edge); background: var(--lt-mount); color: var(--lt-ink);
    transition: background-color 150ms var(--lt-ease), border-color 150ms var(--lt-ease);
}
.ps-btn:hover:not(:disabled) { border-color: var(--lt-ink); }
.ps-btn:active:not(:disabled) { transform: translateY(1px); }
.ps-btn.danger { background: #FBEDEA; border-color: var(--lt-reject); color: var(--lt-reject); }
.ps-btn.danger:hover:not(:disabled) { background: var(--lt-reject); color: #FFFFFF; }
.ps-btn:disabled { opacity: .45; cursor: default; }
.ps-btn:focus-visible, .ps-tile:focus-visible, .ps-vbtn:focus-visible, .ps-vnav:focus-visible {
    outline: 2px solid var(--lt-ink); outline-offset: 2px;
}
.ps-grid {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(156px, 1fr)); gap: 16px 12px;
    user-select: none; min-height: 40px;
}
.ps-head {
    grid-column: 1 / -1; margin-top: 16px; padding: 0 2px; font-size: 15px; font-weight: 600; color: var(--lt-ink);
}
/* a slide mount: off-white frame, small radius, a crisp cool shadow like a mount lying on the glass */
.ps-tile {
    position: relative; padding: 6px; border-radius: 4px; cursor: pointer; background: var(--lt-mount);
    box-shadow: 0 0 0 1px rgba(29, 32, 36, .07), 0 1px 2px rgba(36, 48, 66, .10), 0 6px 14px -8px rgba(36, 48, 66, .28);
    transition: box-shadow 200ms var(--lt-ease), transform 200ms var(--lt-ease);
}
.ps-tile:hover {
    box-shadow: 0 0 0 1px rgba(29, 32, 36, .12), 0 2px 4px rgba(36, 48, 66, .12), 0 14px 24px -12px rgba(36, 48, 66, .38);
}
/* grease pencil: a thick yellow ring around what you picked */
.ps-tile::after {
    content: ""; position: absolute; inset: -5px; border-radius: 8px; pointer-events: none;
    border: 3px solid var(--lt-pick); opacity: 0; transform: scale(1.03);
    transition: opacity 160ms var(--lt-ease), transform 160ms var(--lt-ease);
}
.ps-tile.sel::after { opacity: 1; transform: none; }
.ps-img { position: relative; aspect-ratio: 1; overflow: hidden; border-radius: 2px; background: #DCE1E7; }
.ps-img img { width: 100%; height: 100%; object-fit: cover; display: block; }
.ps-del, .ps-open {
    position: absolute; right: 4px; display: none; border: none; border-radius: 4px; cursor: pointer;
    background: rgba(29, 32, 36, .72); color: #FFFFFF; font-size: 14px; padding: 2px 8px;
}
.ps-del { top: 4px; }
.ps-open { bottom: 4px; }
.ps-del:hover { background: var(--lt-reject); }
.ps-open:hover { background: var(--lt-frame); }
.ps-tile:hover .ps-del, .ps-tile:hover .ps-open { display: block; }
.ps-check {
    position: absolute; top: 4px; left: 4px; display: none; width: 24px; height: 24px; border-radius: 50%;
    background: var(--lt-pick); color: var(--lt-ink); font-size: 14px; font-weight: 700; line-height: 24px; text-align: center;
    box-shadow: 0 1px 3px rgba(0, 0, 0, .3);
}
.ps-tile.sel .ps-check { display: block; }
/* fixed caption height (up to 5 lines: who, collection, file, date and place, extra) -> every mount has the same
   size; each line stays on one line and ends with an ellipsis, the full text is in the tooltip */
.ps-cap {
    box-sizing: content-box; font-size: 12px; line-height: 16px; padding: 6px 2px 0;
    color: var(--lt-soft); height: calc(5 * 16px); overflow: hidden;
}
.ps-line { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

/* full-screen viewer: the photo alone on black, controls on a graphite strip */
.ps-view {
    position: fixed; inset: 0; z-index: 2147483000; background: #000000;
    display: flex; align-items: center; justify-content: center;
}
.ps-view[hidden], .ps-vimg[hidden], .ps-vvid[hidden] { display: none; }
.ps-vimg, .ps-vvid { width: 100vw; height: 100vh; object-fit: contain; }  /* fit the screen, small ones too */
.ps-vtop {
    position: absolute; top: 0; left: 0; right: 0; display: flex; gap: 12px; align-items: center;
    padding: 8px 16px; color: #E6E8EB; font-size: 14px; background: rgba(36, 39, 43, .88);
}
.ps-vcap { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.ps-vbtns, .ps-vacts { display: flex; gap: 8px; flex-shrink: 0; }
.ps-vhint { font-size: 12px; color: #9AA1A9; white-space: nowrap; margin-left: auto; }
.ps-vbtn {
    border: 1px solid #3E4248; border-radius: 6px; padding: 6px 12px; font: inherit; font-size: 14px; font-weight: 500;
    cursor: pointer; background: #33373C; color: #FFFFFF; transition: background-color 150ms var(--lt-ease);
}
.ps-vbtn:hover { background: #41464C; }
.ps-vbtn.danger:hover { background: var(--lt-reject); border-color: var(--lt-reject); }
.ps-vnav {
    position: absolute; top: 50%; transform: translateY(-50%); width: 48px; height: 96px; border: none;
    border-radius: 6px; font-size: 40px; line-height: 1; color: #FFFFFF; background: rgba(36, 39, 43, .55); cursor: pointer;
}
.ps-vnav:hover { background: rgba(36, 39, 43, .9); }
.ps-vprev { left: 16px; }
.ps-vnext { right: 16px; }
.ps-sentinel { height: 1px; }
/* viewer strip: the other shots of the same stack; the image makes room for it */
.ps-view.strip .ps-vimg, .ps-view.strip .ps-vvid { height: calc(100vh - 96px); align-self: flex-start; }
.ps-vstrip {
    position: absolute; left: 0; right: 0; bottom: 0; height: 96px; box-sizing: border-box; padding: 12px 16px;
    display: flex; gap: 8px; justify-content: safe center; overflow-x: auto; scrollbar-width: thin;
    background: rgba(36, 39, 43, .92);
}
.ps-vstrip[hidden] { display: none; }
.ps-vthumb {
    flex: 0 0 auto; width: 72px; height: 72px; padding: 0; border: none; border-radius: 4px; overflow: hidden;
    cursor: pointer; background: #33373C; opacity: .55; outline: 3px solid transparent; outline-offset: 2px;
    transition: opacity 150ms var(--lt-ease), outline-color 150ms var(--lt-ease);
}
.ps-vthumb img { width: 100%; height: 100%; object-fit: cover; display: block; }
.ps-vthumb:hover { opacity: .9; }
.ps-vthumb.cur { opacity: 1; outline-color: var(--lt-pick); }
.ps-band {
    position: fixed; display: none; pointer-events: none; z-index: 1000; border-radius: 2px;
    border: 2px solid var(--lt-pick); background: rgba(242, 183, 5, .14);
}
/* icons: Material Symbols Rounded, the font Streamlit ships (offline) */
.ps-ico {
    font-family: 'Material Symbols Rounded'; font-weight: normal; font-style: normal; font-size: 18px; line-height: 1;
    display: inline-block; vertical-align: -4px; letter-spacing: normal; text-transform: none; white-space: nowrap;
    font-feature-settings: 'liga'; -webkit-font-smoothing: antialiased; user-select: none;
}
.ps-cap .ps-ico, .ps-head .ps-ico, .ps-vcap .ps-ico { font-size: 16px; vertical-align: -3px; }
.ps-btn .ps-ico, .ps-vbtn .ps-ico { margin-right: 4px; }
.ps-vfs .ps-ico, .ps-vclose .ps-ico { margin-right: 0; }
.ps-vnav .ps-ico { font-size: 36px; vertical-align: middle; }
.ps-check .ps-ico { font-size: 18px; vertical-align: -4px; font-weight: 700; }
.ps-del .ps-ico, .ps-open .ps-ico { font-size: 16px; vertical-align: -3px; }

/* motion: only opacity and transform (composited, no layout or repaint) and only when something new appears */
@keyframes lt-lay { from { opacity: 0; transform: translateY(8px) scale(.98); } to { opacity: 1; transform: none; } }
@keyframes lt-fade { from { opacity: 0; } to { opacity: 1; } }
@keyframes lt-zoom { from { opacity: 0; transform: scale(.97); } to { opacity: 1; transform: none; } }
.ps-tile { animation: lt-lay 360ms var(--lt-ease) backwards; animation-delay: calc(min(var(--i, 0), 24) * 16ms); }
.ps-view:not([hidden]) { animation: lt-fade 180ms ease-out both; }
.ps-view:not([hidden]) .ps-vimg:not([hidden]) { animation: lt-zoom 240ms var(--lt-ease) both; }
@media (prefers-reduced-motion: reduce) {
    .ps-root *, .ps-root *::after { transition: none !important; animation: none !important; }
}
"""

_grid = st.components.v2.component("photo_grid", js=_JS, css=_CSS)


def photo_grid(list_id, items, total, has_more, actions, on_more, media=None):
    """Render the grid; returns the action the user just triggered ({"name", "ids"}) or None.

    items:   [{"id": int, "kind": "image"|"video", "img": cache key, "cap": str, "title": str,
               "get_img": () -> jpeg bytes, "head": optional group header shown before the item}]
    actions: [{"name", "label", "label_sel" (with {n}), "need_sel", "danger"}]
    media:   (base_url, token) of media_server for the full-screen viewer
    """
    sent = st.session_state.setdefault("_grid_sent", set())
    out = []
    for it in items:
        d = {"id": it["id"], "kind": it.get("kind", "image"), "img": it["img"], "cap": it["cap"],
             "title": it.get("title", "")}
        if it.get("head"):
            d["head"] = it["head"]
        if it.get("group") is not None:  # stack / copy group: the viewer shows its strip
            d["group"] = it["group"]
        if it["img"] not in sent:
            jpeg = it["get_img"]()
            if jpeg:
                d["src"] = "data:image/jpeg;base64," + base64.b64encode(jpeg).decode()
                sent.add(it["img"])
        out.append(d)
    res = _grid(key=f"grid{list_id}",
                data={"list": list_id, "items": out, "total": total, "has_more": has_more, "actions": actions,
                      "media": media[0] if media else None, "token": media[1] if media else None},
                on_action_change=lambda: None, on_more_change=on_more, on_need_change=lambda: None)
    if res.need:
        sent.difference_update(res.need)
        st.rerun()
    return res.action
