# Brand

The mark is a top-down **ephyra** — the juvenile jellyfish: eight sweeping arms
with a pinwheel lean, each forking at the tip with a bulb, hatch-fans in the
clefts, a ringed clover manubrium at the centre. Neon line-art in the app's
cyan-to-violet palette.

## Files

| File | Use |
|---|---|
| `mark-full.svg` | The detailed line-art mark. Hero / app icon / large use. `web/public/icon.svg` is a copy. |
| `mark-ink.svg` | Same lines in ink, for light backgrounds. |
| `mark-glyph.svg` | Simplified: 8 solid arms + hub, gradient. Use ~24–96px (sidebar, cards). |
| `favicon.svg` | The glyph in flat cyan. Favicons and anything ~16–32px. `web/public/favicon.svg` is a copy. |
| `banner.png` | README header. |

Below ~48px the detailed mark turns to fuzz — switch to `mark-glyph.svg`, and to
`favicon.svg` below ~24px.

The in-app wordmark is `web/src/components/Mark.tsx` (the simplified glyph, inline) next
to "ephyra" in Bricolage.

## Wordmark

```css
.ephyra-wordmark {
  font-family: "Bricolage Grotesque Variable", "Bricolage Grotesque", sans-serif;
  font-weight: 700;
  letter-spacing: -0.03em;
  text-transform: lowercase;
  color: #E8EEF7;   /* #14161C on light */
}
```

Always lowercase. "ephyra", never "Ephyra".

## Rules

- Clear space around a lockup = half the mark's height, every side.
- Minimum mark size 16px; minimum horizontal lockup width 96px.
- Don't recolour the arms, rotate the mark, or set the wordmark in another face.

## Palette

`#06080F` abyss · `#0A0E1B` deep · `#4FE0D8` cyan · `#9B7BFF` violet ·
`#FFC24B` mote · `#E8EEF7` ink
