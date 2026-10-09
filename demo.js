// The interactive demo on the landing page.
//
// The frames are REAL captures of `dejima tui --demo`, produced by
// scripts/capture-demo-frames.py and pre-rendered to HTML spans at capture time.
// Nothing here reimplements the TUI: this file is a projector, and the thing it
// projects is the actual program. Re-run the capture after a TUI change and the
// demo is current — which is the failure a hand-built mock cannot avoid.
//
// WHAT CHANGED, AND WHY. This used to walk six recorded scenes, so only keys on
// a rehearsed path did anything: Escape did nothing, arrows did nothing off the
// script, and the visitor was railroaded through one tour. The capture now
// records a GRAPH — every key the TUI answers, tried from every state it
// reaches — so this file no longer plays a tour. It hands over the keyboard.
//
// Degrades honestly: with JS off, or if the frame data fails to load, the
// section says so rather than sitting blank.
(function () {
  var root = document.getElementById('try-dejima');
  if (!root) return;

  var screen = root.querySelector('[data-screen]');
  var tabsEl = root.querySelector('[data-tabs]');
  var hintEl = root.querySelector('[data-hint]');
  if (!screen || !tabsEl || !hintEl) return;

  var DATA = null;
  var edges = {};        // frameId -> { key -> frameId }
  var rootFrame = null;
  var typed = '';        // what the visitor has typed at the fake shell
  var history = [];      // lines printed above the shell prompt
  var tabs = [];         // [{id, title, kind:'tui'|'session', frame?, body?}]
  // Where the visitor came from, so Escape always has somewhere to go. The
  // capture is breadth-first and bounded, so the deepest states it reached have
  // no recorded edges — without this, walking far enough in lands you on a
  // screen where nothing responds, which is the exact complaint this rewrite
  // exists to fix. Going back is honest there: it is what Escape does in the
  // real TUI, and it is never a screen the visitor has not already seen.
  var backStack = [];
  var active = 0;

  // Labels for keys the TUI answers, used when suggesting what to try. Only
  // keys that actually have an edge from the current frame are ever offered, so
  // this is naming, not promising.
  var LABELS = {
    j: 'j/↓ down', k: 'k/↑ up', Down: '↓ down', Up: '↑ up',
    Left: '←', Right: '→', Space: 'space expand', Enter: '⏎ open',
    Escape: 'esc back', C: 'C connection', s: 's settings', m: 'm actions',
    n: 'n new island', E: 'E collapse all', '?': '? help', '>': '> shell'
  };

  function keyName(e) {
    switch (e.key) {
      case 'ArrowDown': return 'Down';
      case 'ArrowUp': return 'Up';
      case 'ArrowLeft': return 'Left';
      case 'ArrowRight': return 'Right';
      case ' ': return 'Space';
      case 'Enter': return 'Enter';
      case 'Escape': return 'Escape';
      default: return e.key;
    }
  }

  // Accept the graph produced by the current capture, and still work against an
  // older scenes file — a stale demo-frames.json should degrade to the old tour,
  // not to a blank box.
  function buildEdges(d) {
    if (d.edges) { edges = d.edges; rootFrame = d.root; return; }
    edges = {};
    (d.scenes || []).forEach(function (sc) {
      for (var i = 0; i + 1 < sc.steps.length; i++) {
        var from = sc.steps[i].frame, step = sc.steps[i + 1];
        if (!step.key) continue;
        (edges[from] || (edges[from] = {}))[step.key] = step.frame;
      }
    });
    rootFrame = d.scenes && d.scenes[0] && d.scenes[0].steps[0].frame;
  }

  function render() {
    var t = tabs[active];
    if (t.kind === 'tui') {
      screen.innerHTML = DATA.frames[t.frame] || '';
    } else {
      screen.textContent = t.body;
    }
    tabsEl.innerHTML = '';
    tabs.forEach(function (tab, i) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'demo-tab' + (i === active ? ' is-active' : '');
      b.textContent = tab.title;
      b.addEventListener('click', function () { active = i; render(); focusScreen(); });
      tabsEl.appendChild(b);
    });
  }

  function hint(msg) { hintEl.textContent = msg; }

  function available() {
    var t = tabs[active];
    if (t.kind !== 'tui') return 'click the dejima tab to go back';
    var ks = Object.keys(edges[t.frame] || {});
    if (!ks.length) return 'esc back   ·   q quit';
    var s = 'try: ' + ks.slice(0, 7).map(function (k) { return LABELS[k] || k; }).join('   ');
    if (!ks.Escape && backStack.length) s += '   esc back';
    return s + '   ·   q quit';
  }

  // ---- the fake shell -------------------------------------------------------
  //
  // The opening is a terminal with a prompt and nothing else, because that is
  // what the visitor sees on their own machine. `dejima` starts the dashboard;
  // a few other commands answer so that typing something reasonable is not
  // punished with silence, and anything else gets the shell's real reply.
  var CANNED = {
    'dejima --help': [
      'dejima — isolated islands for AI coding agents, on your own hardware',
      '',
      'Usage:  dejima [command]',
      '',
      '  (no args)   open the dashboard',
      '  init        create an island from a repo',
      '  ls          list islands',
      '  agent       add, open, or remove an agent',
      '  secret      store a credential for an island',
      '  egress      see or control an island’s outbound network',
      '  doctor      check this machine',
      '',
      'Run `dejima` with no arguments to open the dashboard.'
    ],
    'dejima ls': [
      'NAME        AGENTS  STATE     MEMORY     REPO',
      'dejima           4  running   1.5 GiB    git@github.com:aoos/dejima.git',
      'janus            2  running   341.5 MiB  git@github.com:aoos/janus.git',
      'wildfire         2  running   605.8 MiB  git@github.com:aoos/wildfire.git'
    ],
    'dejima doctor': [
      'System    docker          OK    server 28.1.1',
      'System    tmux            OK    /opt/homebrew/bin/tmux',
      'Egress    proxy listener  OK    127.0.0.1:7280 — accepting connections',
      'Islands   3 running       OK',
      '',
      'No problems found.'
    ],
    'help': ['Try `dejima` to open the dashboard, or `dejima --help` for the commands.'],
    'ls': ['dejima  janus  wildfire'],
    'whoami': ['you'],
    'pwd': ['/Users/you']
  };

  function renderShell() {
    var out = history.length ? history.join('\n') + '\n' : '';
    screen.textContent = out + '$ ' + typed + '█';
    screen.scrollTop = screen.scrollHeight;
  }

  function runCommand(cmd) {
    history.push('$ ' + cmd);
    var c = cmd.trim();
    if (c === 'dejima') { boot(); return; }
    if (c === 'clear') { history = []; return; }
    if (c === '') { return; }
    var out = CANNED[c];
    if (out) { history = history.concat(out, ''); return; }
    history.push('zsh: command not found: ' + c.split(/\s+/)[0], '');
  }

  function go(frame) {
    var t = tabs[active];
    backStack.push(t.frame);
    t.frame = frame;
    render();
    hint(available());
  }

  function boot() {
    if (!DATA) {
      // Typed `dejima` before the frames arrived. Say so for the moment it
      // takes, rather than swallowing the command.
      hint('starting\u2026');
      ensureData().then(boot).catch(function () {
        history.push('the live demo could not load', '');
        renderShell();
        hint('the live demo could not load');
      });
      return;
    }
    tabs = [{ id: 'tui', title: 'dejima', kind: 'tui', frame: rootFrame }];
    active = 0;
    backStack = [];
    render();
    hint(available());
  }

  // Quitting returns to the shell the visitor started in, with the session's
  // scrollback intact — the same thing `q` does on a real machine.
  function quit() {
    tabs = [];
    backStack = [];
    // runCommand already recorded the `$ dejima` that started this session;
    // pushing it again here printed the command twice on every quit.
    history.push('');
    renderShell();
    hint('type  dejima  and press enter');
  }

  // Opening an agent is the one thing the capture cannot show: attaching needs a
  // daemon, and demo mode has none. So the session tab is openly canned, while
  // every TUI frame stays real. Tabs are the TERMINAL's, which is how Dejima is
  // actually used: the daemon sets their titles over the session protocol.
  function openSession() {
    for (var i = 0; i < tabs.length; i++) {
      if (tabs[i].id === 'a1') { active = i; render(); return; }
    }
    tabs.push({
      id: 'a1', title: 'api-gateway / a1', kind: 'session',
      body: [
        'Attached to api-gateway / a1 — Claude.',
        'Detach with ctrl-b d; the agent keeps running.',
        '',
        '> summarise what changed on this branch',
        '',
        '  Three commits since origin/master. The first moves the retry budget',
        '  out of the request path, the second adds the test that would have',
        '  caught the original hang, and the third is a docs fix.',
        '',
        '  Want me to open a PR?',
        '',
        '(this pane is illustrative — the dejima tab is a real capture)'
      ].join('\n')
    });
    active = tabs.length - 1;
    render();
    hint('click the dejima tab to go back');
  }

  function focusScreen() { screen.focus(); }

  function onKey(e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    var k = keyName(e);

    if (!tabs.length) {                       // the fake shell
      if (k === 'Enter') {
        e.preventDefault();
        var cmd = typed; typed = '';
        runCommand(cmd);
        if (!tabs.length) renderShell();
        return;
      }
      if (e.key === 'Backspace') { e.preventDefault(); typed = typed.slice(0, -1); renderShell(); return; }
      // Swallow the navigation keys here too. The screen has focus, so letting
      // them through scrolls the page out from under a visitor who thinks they
      // are driving a terminal.
      if (k === 'Up' || k === 'Down' || k === 'Left' || k === 'Right' || k === 'Escape') {
        e.preventDefault(); return;
      }
      if (e.key.length === 1) { e.preventDefault(); typed += e.key; renderShell(); }
      return;
    }

    var t = tabs[active];
    if (t.kind !== 'tui') {
      // A session tab has nothing to drive; let the page keep its keys.
      return;
    }
    e.preventDefault();
    press(k);
  }

  // The named-key half of the handler, split out so the on-screen controls in
  // full-window mode drive exactly the same code as a keyboard. A phone has no
  // arrows and no Escape, so without those buttons full-window would be a large
  // picture of a terminal rather than one you can use.
  function press(k) {
    var t = tabs[active];
    if (!t || t.kind !== 'tui') return;

    if (k === 'q') { quit(); return; }

    // Escape prefers what the real TUI did; where the capture stopped short it
    // retraces the visitor's own steps rather than doing nothing.
    if (k === 'Escape' && !(edges[t.frame] || {})['Escape']) {
      if (backStack.length) {
        t.frame = backStack.pop();
        render();
        hint(available());
      } else {
        hint('already at the top — q quits');
      }
      return;
    }

    // Enter on a row with no recorded transition is an attach, and attaching
    // opens a tab — the one branch the capture cannot walk.
    if (k === 'Enter' && !(edges[t.frame] || {})[k]) { openSession(); return; }

    var next = (edges[t.frame] || {})[k];
    if (!next) { hint('nothing bound to that here — ' + available()); return; }
    go(next);
  }

  // Focus mode. Taking the arrow keys and Escape away from the page is a real
  // change of mode, so the page shows it: everything behind the demo recedes
  // while the demo has the keyboard, and comes back when it does not.
  function installFocusMode() {
    var dim = document.createElement('div');
    dim.className = 'demo-dim';
    dim.setAttribute('aria-hidden', 'true');
    document.body.appendChild(dim);
    screen.addEventListener('focus', function () { document.body.classList.add('demo-focus'); });
    screen.addEventListener('blur', function () { document.body.classList.remove('demo-focus'); });
    // Clicking the dimmed page is a way out, not a trap.
    dim.addEventListener('mousedown', function () { screen.blur(); });
  }

  // THE FRAMES LOAD ON FIRST TOUCH, NOT ON PAGE LOAD.
  //
  // Every frame is a full 132x40 screen of styled spans — about 10 KB each, and
  // the graph has enough of them to matter. The demo sits near the top of the
  // landing page, so fetching eagerly would put a megabyte in front of every
  // visitor, including the ones who scroll straight past. The opening screen is
  // a shell prompt this file can draw by itself, so nothing needs to be
  // downloaded until someone actually clicks in.
  var loading = null;
  function ensureData() {
    if (loading) return loading;
    loading = fetch('demo-frames.json').then(function (r) { return r.json(); }).then(function (d) {
      buildEdges(d);
      if (!rootFrame || !d.frames[rootFrame]) throw new Error('no root frame');
      DATA = d;
      root.classList.add('is-live');
      scheduleFit();
      return d;
    });
    return loading;
  }

  // FULL-WINDOW ON A PHONE, WITH CONTROLS.
  // The frame is 170 columns; a portrait phone gives each character under four
  // pixels. Tapping fills the window and turns the frame on its side. The
  // controls are not decoration: a phone keyboard has no arrows and no Escape,
  // so without them this would be a large picture of a terminal.
  function isPhone() {
    try { return window.matchMedia('(max-width: 820px), (pointer: coarse)').matches; }
    catch (e) { return false; }
  }

  function installFullWindow() {
    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'demo-close';
    close.setAttribute('aria-label', 'Close the demo');
    close.textContent = '\u00d7';
    document.body.appendChild(close);

    var keys = document.createElement('div');
    keys.className = 'demo-keys';
    [['\u2191', 'Up'], ['\u2193', 'Down'], ['\u23ce', 'Enter'], ['esc', 'Escape'], ['q', 'q']]
      .forEach(function (pair) {
        var b = document.createElement('button');
        b.type = 'button';
        b.textContent = pair[0];
        b.addEventListener('click', function (ev) { ev.stopPropagation(); press(pair[1]); });
        keys.appendChild(b);
      });
    document.body.appendChild(keys);

    function exit() {
      document.body.classList.remove('demo-full');
      document.body.classList.remove('demo-focus');
    }
    close.addEventListener('click', exit);
    // Escape leaves full-window before it reaches the TUI: to someone whose
    // phone has just filled with a terminal, that is what the key means.
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && document.body.classList.contains('demo-full')) { exit(); }
    });

    screen.addEventListener('click', function () {
      if (!isPhone() || document.body.classList.contains('demo-full')) return;
      document.body.classList.add('demo-full');
      scheduleFit();
      // Nobody opens this to look at a shell prompt they cannot type at, so a
      // phone goes straight to the dashboard.
      ensureData().then(function () { if (!tabs.length) boot(); });
    });
  }

  // FIT THE TYPE TO THE FRAME BY MEASURING IT, not by assuming a cell width.
  //
  // The CSS first did this with a constant: font-size = width / (cols * 0.62).
  // A monospace advance is near 0.6em in the fonts this was written against,
  // but the stack falls back per platform -- Consolas on Windows is about
  // 0.55em -- and the error lands as dead space on the right of the frame,
  // which is exactly how it was reported. The browser knows the real number, so
  // ask it: render a known string, divide, and scale. Re-measured on resize
  // because the fallback can change with the width (and because the container
  // does).
  var fitRaf = 0;
  function fitType() {
    if (!DATA) return;
    var cols = DATA.cols || 170;
    var probe = document.createElement('span');
    probe.textContent = new Array(101).join('M'); // 100 chars
    probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre;font-size:100px';
    probe.style.fontFamily = getComputedStyle(screen).fontFamily;
    screen.appendChild(probe);
    var per = probe.getBoundingClientRect().width / 100 / 100; // em per char
    screen.removeChild(probe);
    if (!(per > 0)) return;
    var cs = getComputedStyle(screen);
    var avail = screen.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    if (!(avail > 0)) return;
    var size = avail / (cols * per);
    screen.style.fontSize = Math.max(6, Math.floor(size * 100) / 100) + 'px';
  }
  function scheduleFit() {
    if (fitRaf) return;
    fitRaf = requestAnimationFrame(function () { fitRaf = 0; fitType(); });
  }
  window.addEventListener('resize', scheduleFit);
  window.addEventListener('orientationchange', scheduleFit);

  screen.setAttribute('tabindex', '0');
  screen.addEventListener('keydown', onKey);
  screen.addEventListener('click', function () { ensureData(); focusScreen(); });
  screen.addEventListener('focus', ensureData);
  installFocusMode();
  installFullWindow();
  renderShell();
  hint('click in, then type  dejima  and press enter');
})();
