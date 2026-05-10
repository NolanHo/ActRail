# ActRail Theme System

## Design Skill Inputs

The 2026-05 theme refresh used `/root/code/ui-ux-pro-max-skill` with these queries:

- `developer tool IDE agent console long reading bilingual dark mode --design-system`
- `dark mode long reading accessibility --domain style`
- `bilingual reading typography professional code --domain typography`
- `developer tool dark mode accessibility contrast --domain color`

The relevant recommendation is a developer-tool / IDE dark theme with high contrast,
semantic color tokens, limited decorative effects, and typography tuned for long
reading.

## Default Theme

The default ActRail theme is **Graphite Ink**.

Graphite Ink is a dark workbench theme for long English and Chinese mixed
reading. It favors graphite blue-gray surfaces, subdued borders, and a small
functional accent set:

- teal for focus, links, and running activity
- green for completed/healthy state
- amber for waits and attention
- red for destructive or failed state

Avoid using large colored fills for semantic states. Prefer one of:

- a 2px accent rail
- an icon or status dot
- a subtle border
- concise text

## Typography

Use separate font roles:

- UI: neutral sans stack
- assistant prose: readable bilingual sans stack
- code/data: monospaced stack

Long-form assistant text should stay near 16px with line-height around 1.55 to
1.62. Avoid pure white body text on dark surfaces; use off-white foregrounds.

## Implementation Rules

- Components should consume semantic CSS variables, not Tailwind raw colors for
  theme surfaces.
- Dark mode should not mix surfaces toward `white`; use surface/elevation tokens.
- Purple and neon effects are not part of the default theme.
- Tool, reasoning, wait, and error cards should keep the same dark base surface
  and communicate state through small accents.
