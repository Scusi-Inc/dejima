// The interactive demo on the landing page.
//
// The frames are REAL captures of `dejima tui --demo`, produced by
// scripts/capture-demo-frames.py and pre-rendered to HTML spans at capture time.
// Nothing here reimplements the TUI: this file is a projector, and the thing it
// projects is the actual program. Re-run the capture after a TUI change and the
// demo is current — which is the failure a hand-built mock cannot avoid, and
// which herdr's own landing page documents in itself.
//
// Degrades honestly: with JS off the section renders its first frame as static
// HTML and the prompt line explains that the live version needs JS.
(function () {
  var root = document.getElementById('try-dejima');
  if (!root) return;

  var screen = root.querySelector('[data-screen]');
  var tabsEl = root.querySelector('[data-tabs]');
  var hintEl = root.querySelector('[data-hint]');
  if (!screen || !tabsEl || !hintEl) return;

  var DATA = null;
  var edges = {};        // frameId -> { key -> frameId }
  var typed = '';        // what the visitor has typed at the fake shell
  var tabs = [];         // [{id, title, kind:'tui'|'session', frame?, body?}]
  var active = 0;

  // Keys the TUI answers, in the order a visitor should try them. Shown when a
  // key does nothing, because a dead keystroke on a landing page reads as broken
  // rather than as out of scope.
  var LABELS = {
    j: 'j / ↓ down', k: 'k / ↑ up', Space: 'space expand', Enter: '⏎ open',
    Escape: 'esc back', C: 'C switch connection', s: 's secrets',
    n: 'n new island', '?': '? help'
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

  // Build the transition graph from the recorded scenes. A frame reached by two
  // scenes is one node, so a visitor who wanders from one flow into another
  // keeps working rather than hitting the end of a script.
  function buildEdges(d) {
    d.scenes.forEach(function (sc) {
      for (var i = 0; i + 1 < sc.steps.length; i++) {
        var from = sc.steps[i].frame, step = sc.steps[i + 1];
        if (!step.key) continue;
        (edges[from] || (edges[from] = {}))[step.key] = step.frame;
      }
    });
  }

  function tuiTab() { return tabs[0]; }

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

  function hint(msg) {
    hintEl.textContent = msg;
  }

  function available() {
    var t = tabs[active];
    if (t.kind !== 'tui') return 'click the dejima tab to go back';
    var ks = Object.keys(edges[t.frame] || {});
    if (!ks.length) return 'nothing further down this path — press esc';
    return 'try: ' + ks.map(function (k) { return LABELS[k] || k; }).join('   ');
  }

  // The fake shell. A blank terminal and one instruction is the whole opening —
  // the visitor types the same thing they would type on their own machine.
  function renderShell() {
    screen.textContent = '$ ' + typed + '█';
    hint('type  dejima  and press enter');
  }

  function boot() {
    tabs = [{ id: 'tui', title: 'dejima', kind: 'tui', frame: DATA.scenes[0].steps[0].frame }];
    active = 0;
    render();
    hint(available());
  }

  // Opening an agent is the one thing the capture cannot show: attaching needs a
  // daemon, and demo mode has none. So the session tab is openly canned — the
  // operator asked for exactly that — while every TUI frame stays real. Tabs are
  // the TERMINAL's, which is how Dejima is actually used: the daemon sets their
  // titles over the session protocol.
  function openSession() {
    var existing = tabs.findIndex(function (t) { return t.id === 'a1'; });
    if (existing > -1) { active = existing; render(); return; }
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
        '(this pane is illustrative — the frames in the dejima tab are real captures)'
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
      e.preventDefault();
      if (k === 'Enter') {
        if (typed.trim() === 'dejima') { boot(); } else { typed = ''; renderShell(); }
        return;
      }
      if (e.key === 'Backspace') { typed = typed.slice(0, -1); renderShell(); return; }
      if (e.key.length === 1) { typed += e.key; renderShell(); }
      return;
    }

    var t = tabs[active];
    if (t.kind !== 'tui') { return; }
    e.preventDefault();

    // Enter on an agent row is an attach, and attaching opens a tab.
    if (k === 'Enter' && !(edges[t.frame] || {})[k]) { openSession(); return; }

    var next = (edges[t.frame] || {})[k];
    if (!next) { hint('that key is not in this demo — ' + available()); return; }
    t.frame = next;
    render();
    hint(available());
  }

  fetch('demo-frames.json').then(function (r) { return r.json(); }).then(function (d) {
    DATA = d;
    buildEdges(d);
    root.classList.add('is-live');
    screen.setAttribute('tabindex', '0');
    screen.addEventListener('keydown', onKey);
    screen.addEventListener('click', focusScreen);
    renderShell();
  }).catch(function () {
    hint('the live demo could not load — the screenshot above is the real thing');
  });
})();
