# Brand

The mark is a top-down **ephyra** — the juvenile jellyfish, eight arms each
forking into two, around a central mouth. It's the app's palette: cyan to violet,
with the amber mote at the centre.

## Files

| File | Use |
|---|---|
| `mark-full.svg` | Full colour (gradient + amber mote). Default. `web/public/icon.svg` is a copy. |
| `mark-flat.svg` | Single cyan, mote knocked out in abyss. For favicons / anything below ~24px. `web/public/favicon.svg` is a copy. |
| `mark-ink.svg` | Monochrome, for light backgrounds. |
| `mark-outline.svg` | Stroke only. |

The in-app wordmark is `web/src/components/Mark.tsx` (the full mark, inline) next
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
