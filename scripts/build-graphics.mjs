// Build the two authored site graphics as PNGs from inline SVG:
//   - assets/architecture.png  (A5) — host / island / agent topology
//   - assets/brand-island.png  (A6) — the 1636 fan-island + gated bridge + ledger
//
//   npm i @resvg/resvg-js        # one-off; not vendored
//   node scripts/build-graphics.mjs
//
// Each also writes a .svg source next to the .png so the art stays editable.
// Palette mirrors styles.css; fonts are the DejaVu set shipped on most Linux.
import { Resvg } from '@resvg/resvg-js'
import { writeFileSync } from 'node:fs'

const C = {
  bg: '#0f1e35', fg: '#eef3ff', muted: '#94a3b8',
  border: '#1c3358', card: '#122443', code: '#0a172d', accent: '#9ec1ff',
}
const FONTS = [
  '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf',
  '/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf',
  '/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf',
]
const render = (svg, w, out) => {
  writeFileSync(new URL(`../assets/${out}.svg`, import.meta.url), svg)
  const png = new Resvg(svg, {
    fitTo: { mode: 'width', value: w },
    font: { fontFiles: FONTS, defaultFontFamily: 'DejaVu Sans', loadSystemFonts: false },
  }).render().asPng()
  writeFileSync(new URL(`../assets/${out}.png`, import.meta.url), png)
  console.log(`wrote assets/${out}.png (${png.length} bytes)`)
}

/* ───────────────────────── A5 — architecture ───────────────────────── */
const agentChip = (x, y, id, name) => `
  <g transform="translate(${x},${y})">
    <rect width="372" height="46" rx="7" fill="${C.bg}" stroke="${C.border}"/>
    <text x="18" y="29" font-family="DejaVu Sans Mono" font-size="17" fill="${C.muted}">${id}</text>
    <text x="62" y="29" font-family="DejaVu Sans Mono" font-size="17" fill="${C.accent}">${name}</text>
  </g>`

const island = (x, label, agents) => `
  <g transform="translate(${x},250)">
    <rect width="416" height="270" rx="10" fill="${C.code}" stroke="${C.border}"/>
    <text x="24" y="40" font-size="20" font-weight="bold" fill="${C.fg}">${label}</text>
    ${agents.map((a, i) => agentChip(22, 62 + i * 58, a[0], a[1])).join('')}
  </g>`

const ARCH_W = 1120, ARCH_H = 568
const arch = `<svg xmlns="http://www.w3.org/2000/svg" width="${ARCH_W}" height="${ARCH_H}" viewBox="0 0 ${ARCH_W} ${ARCH_H}">
  <rect width="${ARCH_W}" height="${ARCH_H}" fill="${C.bg}"/>
  <g font-family="DejaVu Sans">
    <!-- you drive it -->
    ${['CLI', 'TUI', 'your app'].map((t, i) => `
      <g transform="translate(${312 + i * 172},34)">
        <rect width="148" height="48" rx="8" fill="${C.card}" stroke="${C.border}"/>
        <text x="74" y="31" font-size="19" font-weight="bold" fill="${C.fg}" text-anchor="middle">${t}</text>
      </g>`).join('')}
    <text x="1080" y="64" font-size="16" fill="${C.muted}" text-anchor="end">you drive it</text>
    <!-- link down -->
    <line x1="560" y1="82" x2="560" y2="172" stroke="${C.accent}" stroke-width="2"/>
    <path d="M 553 162 L 560 174 L 567 162 Z" fill="${C.accent}"/>
    <text x="582" y="132" font-size="16" fill="${C.muted}">websocket · HTTP API — over Tailscale</text>
    <!-- host -->
    <rect x="40" y="180" width="1040" height="362" rx="14" fill="${C.card}" stroke="${C.border}" stroke-width="1.5"/>
    <text x="64" y="222" font-size="22" font-weight="bold" fill="${C.accent}">Dejima host</text>
    <text x="214" y="222" font-size="19" fill="${C.muted}">— Mac mini / VPS / cloud VM</text>
    ${island(80, 'island: web', [['a1', 'claude-code'], ['a2', 'openclaw'], ['a3', 'shell']])}
    ${island(624, 'island: api', [['a1', 'codex'], ['a2', 'letta'], ['a3', 'headless']])}
  </g>
</svg>`
render(arch, ARCH_W, 'architecture')

/* ───────────────────── A6 — brand: the fan island ───────────────────── */
const BR_W = 1120, BR_H = 470
// fan ribs from the hinge out to the curved outer edge
const ribs = [70, 140, 220, 300, 380, 430]
  .map(y => `<line x1="416" y1="250" x2="${660 + Math.sin((y - 250) / 360) * 30}" y2="${y}" stroke="${C.border}" stroke-width="1.4" opacity="0.7"/>`)
  .join('')
const brand = `<svg xmlns="http://www.w3.org/2000/svg" width="${BR_W}" height="${BR_H}" viewBox="0 0 ${BR_W} ${BR_H}">
  <rect width="${BR_W}" height="${BR_H}" fill="${C.bg}"/>
  <!-- harbor rings behind the island -->
  <g transform="translate(560,250)" fill="none" stroke="${C.border}">
    <circle r="150" opacity="0.30"/><circle r="240" opacity="0.20"/><circle r="330" opacity="0.12"/>
  </g>
  <g font-family="DejaVu Sans">
    <!-- mainland: your machine -->
    <path d="M 0 60 L 250 60 Q 285 130 250 200 L 285 250 L 250 300 Q 285 370 250 410 L 0 410 Z"
          fill="${C.card}" stroke="${C.border}"/>
    <g transform="translate(95,205)">
      <rect x="-44" y="-30" width="88" height="60" rx="6" fill="${C.code}" stroke="${C.accent}" stroke-width="1.6"/>
      <line x1="-44" y1="-8" x2="44" y2="-8" stroke="${C.accent}" stroke-width="1.2" opacity="0.6"/>
      <circle cx="0" cy="11" r="4" fill="${C.accent}"/>
    </g>
    <text x="95" y="285" font-size="17" font-weight="bold" fill="${C.fg}" text-anchor="middle">your machine</text>
    <text x="95" y="308" font-size="14" fill="${C.muted}" text-anchor="middle">files · credentials</text>

    <!-- the bridge: one gated crossing (the broker) -->
    <line x1="285" y1="241" x2="404" y2="241" stroke="${C.accent}" stroke-width="2"/>
    <line x1="285" y1="259" x2="404" y2="259" stroke="${C.accent}" stroke-width="2"/>
    <line x1="318" y1="241" x2="318" y2="259" stroke="${C.border}" stroke-width="1.4"/>
    <line x1="371" y1="241" x2="371" y2="259" stroke="${C.border}" stroke-width="1.4"/>
    <!-- gate at the island end -->
    <rect x="392" y="226" width="26" height="48" rx="4" fill="${C.code}" stroke="${C.accent}" stroke-width="1.8"/>
    <rect x="401" y="242" width="8" height="16" rx="2" fill="none" stroke="${C.accent}" stroke-width="1.4"/>
    <text x="346" y="214" font-size="15" font-weight="bold" fill="${C.accent}" text-anchor="middle">broker</text>
    <text x="346" y="300" font-size="14" fill="${C.muted}" text-anchor="middle">one gated crossing</text>

    <!-- the fan-shaped island -->
    ${ribs}
    <path d="M 416 205 L 660 70 Q 742 250 660 430 L 416 295 Z"
          fill="${C.code}" stroke="${C.accent}" stroke-width="2.2" opacity="0.97"/>
    <!-- agents on the island -->
    ${[['a1', 150], ['a2', 250], ['a3', 350]].map(([id, y]) => `
      <g transform="translate(520,${y})">
        <circle r="13" fill="${C.bg}" stroke="${C.accent}" stroke-width="1.8"/>
        <text y="5" font-size="12" font-family="DejaVu Sans Mono" fill="${C.accent}" text-anchor="middle">${id}</text>
      </g>`).join('')}
    <text x="600" y="250" font-size="15" fill="${C.muted}">agents, contained</text>
    <text x="585" y="455" font-size="17" font-weight="bold" fill="${C.fg}" text-anchor="middle">Dejima — the contained island</text>

    <!-- ledger: the harbor record -->
    <g transform="translate(815,150)">
      <rect width="150" height="118" rx="8" fill="${C.card}" stroke="${C.border}"/>
      ${[26, 50, 74, 98].map(y => `<line x1="20" y1="${y}" x2="118" y2="${y}" stroke="${C.muted}" stroke-width="2" opacity="0.7"/>`).join('')}
      ${[26, 50, 74, 98].map(y => `<circle cx="130" cy="${y}" r="3.2" fill="${C.accent}"/>`).join('')}
    </g>
    <text x="890" y="296" font-size="15" font-weight="bold" fill="${C.accent}" text-anchor="middle">every crossing →</text>
    <text x="890" y="317" font-size="14" fill="${C.muted}" text-anchor="middle">tamper-evident ledger</text>
  </g>
</svg>`
render(brand, BR_W, 'brand-island')

/* ──────────── tmux+SSH vs Dejima — the before/after for the guide ──────────── */
const TV_W = 1120, TV_H = 500
const panel = (x, title, sub) => `
  <rect x="${x}" y="70" width="512" height="400" rx="12" fill="${C.card}" stroke="${C.border}" stroke-width="1.5"/>
  <text x="${x + 26}" y="104" font-size="20" font-weight="bold" fill="${C.fg}">${title}</text>
  <text x="${x + 26}" y="128" font-size="15" fill="${C.muted}">${sub}</text>`

// LEFT — tmux + SSH: one open box, everything reaches everything.
const L = 24
const agentChipL = (cx, cy) => `
  <rect x="${cx - 56}" y="${cy - 22}" width="112" height="44" rx="7" fill="${C.bg}" stroke="${C.border}"/>`
const leftAgents = [[L + 116, 196], [L + 256, 196], [L + 396, 196]]
const filesNodeL = { x: L + 130, y: 360, w: 252, h: 56 }
const tmuxVsDejima = `<svg xmlns="http://www.w3.org/2000/svg" width="${TV_W}" height="${TV_H}" viewBox="0 0 ${TV_W} ${TV_H}">
  <rect width="${TV_W}" height="${TV_H}" fill="${C.bg}"/>
  <g font-family="DejaVu Sans">
    <text x="${L + 6}" y="46" font-size="16" font-weight="bold" fill="${C.muted}" letter-spacing="0.04em">TODAY — tmux + SSH</text>
    <text x="${584 + 6}" y="46" font-size="16" font-weight="bold" fill="${C.accent}" letter-spacing="0.04em">WITH DEJIMA</text>
    ${panel(L, 'your Mac mini', 'one shell, one box, no walls')}
    ${panel(584, 'your Mac mini', 'Dejima daemon')}

    <!-- left: open reach — every agent to the files node, and to each other -->
    <line x1="${leftAgents[0][0]}" y1="218" x2="${leftAgents[2][0]}" y2="218" stroke="${C.muted}" stroke-width="1.4" opacity="0.5" stroke-dasharray="3 5"/>
    ${leftAgents.map(([cx, cy]) => `
      <line x1="${cx}" y1="${cy + 22}" x2="${filesNodeL.x + filesNodeL.w / 2}" y2="${filesNodeL.y}" stroke="${C.muted}" stroke-width="1.4" opacity="0.6"/>
      ${agentChipL(cx, cy)}
      <text x="${cx}" y="${cy + 5}" font-size="14" font-family="DejaVu Sans Mono" fill="${C.accent}" text-anchor="middle">claude</text>`).join('')}
    <rect x="${filesNodeL.x}" y="${filesNodeL.y}" width="${filesNodeL.w}" height="${filesNodeL.h}" rx="7" fill="${C.bg}" stroke="${C.muted}" stroke-width="1.4"/>
    <text x="${filesNodeL.x + filesNodeL.w / 2}" y="${filesNodeL.y + 34}" font-size="15" fill="${C.fg}" text-anchor="middle">your files · ~/.ssh · API tokens</text>

    <!-- right: three sealed islands, one gated crossing, a ledger -->
    ${[0, 1, 2].map(i => {
      const x = 610 + i * 156
      return `
      <rect x="${x}" y="160" width="140" height="86" rx="8" fill="${C.code}" stroke="${C.accent}" stroke-width="1.8"/>
      <text x="${x + 14}" y="184" font-size="13" font-family="DejaVu Sans Mono" fill="${C.muted}">a${i + 1}</text>
      <text x="${x + 70}" y="218" font-size="14" font-family="DejaVu Sans Mono" fill="${C.accent}" text-anchor="middle">agent</text>`
    }).join('')}
    <!-- collapse the three islands to a single gate -->
    ${[0, 1, 2].map(i => `<line x1="${680 + i * 156}" y1="246" x2="840" y2="300" stroke="${C.border}" stroke-width="1.4"/>`).join('')}
    <rect x="812" y="292" width="56" height="40" rx="6" fill="${C.bg}" stroke="${C.accent}" stroke-width="1.8"/>
    <rect x="833" y="304" width="14" height="20" rx="2" fill="none" stroke="${C.accent}" stroke-width="1.4"/>
    <text x="840" y="356" font-size="14" font-weight="bold" fill="${C.accent}" text-anchor="middle">broker</text>
    <line x1="840" y1="332" x2="840" y2="392" stroke="${C.accent}" stroke-width="1.6"/>
    <rect x="690" y="392" width="252" height="50" rx="7" fill="${C.bg}" stroke="${C.muted}" stroke-width="1.4"/>
    <text x="816" y="422" font-size="15" fill="${C.fg}" text-anchor="middle">your files — granted read-only</text>
    <!-- ledger -->
    <g transform="translate(986,292)">
      <rect width="88" height="100" rx="7" fill="${C.code}" stroke="${C.border}"/>
      ${[24, 46, 68, 90].map(y => `<line x1="14" y1="${y}" x2="74" y2="${y}" stroke="${C.muted}" stroke-width="2" opacity="0.7"/>`).join('')}
    </g>
    <text x="1030" y="412" font-size="13" fill="${C.muted}" text-anchor="middle">ledger</text>

    <!-- captions -->
    <text x="${L + 6}" y="496" font-size="14" fill="${C.muted}">Every agent can read everything. No record of what it touched.</text>
    <text x="${584 + 6}" y="496" font-size="14" fill="${C.muted}">Deny-all. One gated crossing, granted read-only, every crossing logged.</text>
  </g>
</svg>`
render(tmuxVsDejima, TV_W, 'tmux-vs-dejima')
