// Build the social/OG card (1200x630) as a PNG from an inline SVG.
//
//   npm i @resvg/resvg-js        # one-off; not vendored
//   node scripts/build-og-card.mjs
//
// Writes assets/og-card.svg (editable source) + assets/og-card.png (the asset
// wired into <meta property="og:image">). Palette mirrors styles.css. Uses the
// DejaVu fonts shipped on most Linux boxes; swap in a brand font if available.
import { Resvg } from '@resvg/resvg-js'
import { writeFileSync } from 'node:fs'

const W = 1200, H = 630
const C = {
  bg: '#0f1e35', fg: '#eef3ff', muted: '#94a3b8',
  border: '#1c3358', card: '#122443', accent: '#9ec1ff',
}

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

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
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
    <text x="78" y="296" font-size="58" font-weight="bold" fill="${C.fg}">Infrastructure for your agents.</text>
    <text x="78" y="370" font-size="58" font-weight="bold" fill="${C.accent}">None of the worry.</text>
    <!-- sub -->
    <text x="80" y="446" font-size="27" fill="${C.muted}">Run a fleet of AI coding agents on hardware you own.</text>
    <!-- footer line -->
    <text x="80" y="566" font-size="23" font-weight="bold" fill="${C.muted}" letter-spacing="0.5">free  ·  open source  ·  self-hosted</text>
    <text x="1120" y="566" font-size="23" font-weight="bold" fill="${C.accent}" text-anchor="end">dejima.tech</text>
  </g>
</svg>`

writeFileSync(new URL('../assets/og-card.svg', import.meta.url), svg)

const resvg = new Resvg(svg, {
  fitTo: { mode: 'width', value: W },
  font: {
    fontFiles: [
      '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf',
      '/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf',
    ],
    defaultFontFamily: 'DejaVu Sans',
    loadSystemFonts: false,
  },
})
const png = resvg.render().asPng()
writeFileSync(new URL('../assets/og-card.png', import.meta.url), png)
console.log(`wrote assets/og-card.png (${png.length} bytes)`)
