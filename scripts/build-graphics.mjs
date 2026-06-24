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
