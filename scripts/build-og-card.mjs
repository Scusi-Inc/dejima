// Build the social/OG cards (1200x630) as PNGs from an inline SVG.
//
//   npm i @resvg/resvg-js        # one-off; not vendored
//   node scripts/build-og-card.mjs            # builds every card below
//
// Each card writes assets/<out>.svg (editable source) + assets/<out>.png (the
// asset wired into <meta property="og:image">). Palette mirrors styles.css.
// To add a guide/post card, add an entry to CARDS — same house style, free.
import { Resvg } from '@resvg/resvg-js'
import { writeFileSync } from 'node:fs'

const W = 1200, H = 630
const C = {
  bg: '#0f1e35', fg: '#eef3ff', muted: '#94a3b8',
  border: '#1c3358', card: '#122443', accent: '#9ec1ff',
}

// One card per page. line1 is white, line2 is accent. Keep each line short
// enough to clear the island motif (roughly <= 26 chars at 58px).
const CARDS = [
  {
    out: 'og-card',
    line1: 'Infrastructure for your agents.',
    line2: 'None of the worry.',
    sub: 'Run a fleet of AI coding agents on hardware you own.',
  },
  {
    out: 'og-tmux-ssh',
    line1: 'Still SSHing into tmux',
    line2: 'to run your agents?',
    sub: 'There is a contained way to run a fleet on your own box.',
  },
  {
    out: 'og-mac-mini',
    line1: 'Turn a Mac mini into',
    line2: 'an AI agent server.',
    sub: 'Claude Code, Codex, and OpenClaw, contained. Up in 5 minutes.',
  },
  {
    out: 'og-vs-coder',
    line1: 'Coder vs Dejima',
    line2: 'Side by side.',
    sub: 'Platform-team CDEs, or one command on your own box.',
  },
  {
    out: 'og-vs-daytona',
    line1: 'Daytona vs Dejima',
    line2: 'Side by side.',
    sub: 'Ephemeral code sandboxes, or a fleet you run and watch.',
  },
  {
    out: 'og-vs-e2b',
    line1: 'E2B vs Dejima',
    line2: 'Side by side.',
    sub: 'An agent-sandbox cloud, or self-hosted on your hardware.',
  },
  {
    out: 'og-linux-server',
    line1: 'Run agents on',
    line2: 'a Linux server.',
    sub: 'Headless on a VPS or a box in the closet. One command.',
  },
  {
    out: 'og-cloud-vm',
    line1: 'Run agents on',
    line2: 'your own cloud VM.',
    sub: 'Self-hosted in your account. Your code, your egress, no meter.',
  },
  {
    out: 'og-local',
    line1: 'Run agents locally,',
    line2: 'contained.',
    sub: 'On the machine you already use, walled off from your files.',
  },
]

// Island / harbor motif, bottom-right: concentric arcs (water) + a dome
// (island) reached by a short bridge (the brokered crossing).
const motif = `
  <g transform="translate(980,470)" fill="none" stroke="${C.border}" stroke-width="2">
    <circle r="70" opacity="0.55"/>
    <circle r="120" opacity="0.40"/>
    <circle r="175" opacity="0.28"/>
    <circle r="235" opacity="0.18"/>
  </g>
  <g transform="translate(980,470)">
    <path d="M -46 8 A 46 46 0 0 1 46 8 Z" fill="${C.card}" stroke="${C.accent}" stroke-width="2.5"/>
    <line x1="-150" y1="8" x2="-46" y2="8" stroke="${C.accent}" stroke-width="2.5" stroke-dasharray="2 7" stroke-linecap="round"/>
    <line x1="-150" y1="8" x2="-150" y2="-34" stroke="${C.border}" stroke-width="2.5"/>
  </g>`

const cardSvg = ({ line1, line2, sub }) => `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <rect width="${W}" height="${H}" fill="${C.bg}"/>
  <rect x="0" y="0" width="${W}" height="6" fill="${C.accent}" opacity="0.9"/>
  ${motif}
  <g font-family="DejaVu Sans">
    <!-- brand -->
    <g transform="translate(80,86)">
      <circle cx="11" cy="6" r="13" fill="none" stroke="${C.accent}" stroke-width="2.5"/>
      <path d="M -3 6 A 14 14 0 0 1 25 6 Z" fill="${C.accent}"/>
      <text x="44" y="15" font-size="30" font-weight="bold" fill="${C.fg}" letter-spacing="0.5">Dejima</text>
    </g>
    <!-- headline -->
    <text x="78" y="296" font-size="58" font-weight="bold" fill="${C.fg}">${line1}</text>
    <text x="78" y="370" font-size="58" font-weight="bold" fill="${C.accent}">${line2}</text>
    <!-- sub -->
    <text x="80" y="446" font-size="27" fill="${C.muted}">${sub}</text>
    <!-- footer line -->
    <text x="80" y="566" font-size="23" font-weight="bold" fill="${C.muted}" letter-spacing="0.5">free  ·  open source  ·  self-hosted</text>
    <text x="1120" y="566" font-size="23" font-weight="bold" fill="${C.accent}" text-anchor="end">dejima.tech</text>
  </g>
</svg>`

const FONTS = [
  '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf',
  '/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf',
]
for (const card of CARDS) {
  const svg = cardSvg(card)
  writeFileSync(new URL(`../assets/${card.out}.svg`, import.meta.url), svg)
  const png = new Resvg(svg, {
    fitTo: { mode: 'width', value: W },
    font: { fontFiles: FONTS, defaultFontFamily: 'DejaVu Sans', loadSystemFonts: false },
  }).render().asPng()
  writeFileSync(new URL(`../assets/${card.out}.png`, import.meta.url), png)
  console.log(`wrote assets/${card.out}.png (${png.length} bytes)`)
}
