# Savvy CRM product UI theme

The authenticated CRM and the public marketing/auth experience are one brand,
but they serve different reading modes. They share Savvy's identity while the
CRM uses a quieter, denser product system suited to daily work. Do not copy
editorial landing-page typography, oversized spacing, or promotional card
treatments into operational screens.

## Source of truth

- `apps/web/tailwind.config.js` owns the application color, type, radius, and
  shadow tokens.
- `apps/web/src/index.css` owns browser-level defaults and accessibility
  behavior.
- Components in `apps/web/src/components/ui` own reusable controls. Feature
  pages should compose these rather than reproduce button, input, table,
  dialog, or state styles.

## Palette

| Role            | Token                    | Value     |
| --------------- | ------------------------ | --------- |
| Main canvas     | `paper` / `slate-50`     | `#f7f8f6` |
| Raised surface  | `paper-bright` / `white` | `#ffffff` |
| Primary ink     | `ink` / `slate-900`      | `#1e231f` |
| Navigation ink  | `slate-950`              | `#111713` |
| Primary signal  | `brand-600`              | `#bb431d` |
| Supporting sage | `sage`                   | `#dde3d7` |
| Muted copy      | `slate-500`              | `#677067` |

Terracotta is a signal, not a page background: use it for active markers,
eyebrows, links, focus rings, and selected states. Deep ink is the default
primary action and navigation color. Success, warning, and danger retain their
semantic colors and must not be replaced by brand orange.

## Typography

- Body, controls, tables, and dense operational copy use `font-sans`.
- Page, section, dialog, and card titles use `font-sans` with a clear weight hierarchy.
- Serif display type is reserved for the public marketing/auth experience.
- Small uppercase labels use strong contrast and generous tracking; do not use
  opacity alone when it would fail WCAG contrast.

## Shape and elevation

- Buttons and inputs use compact `rounded-lg` geometry.
- Tables, cards, menus, and empty states normally use `rounded-xl`.
- Ordinary surfaces rely on a border and at most `shadow-sm`; stronger shadows
  are reserved for overlays and floating menus.
- Density is part of the theme: avoid padding larger than needed to scan or
  operate the content comfortably.

## Application shell

- Desktop navigation is deep ink and grouped as Workspace, Follow-up, and
  Manage.
- Mobile uses the same navigation as an off-canvas drawer; never render a
  second duplicate nav.
- `#main-content` is the scroll container and remains keyboard-focusable.
- Route changes reset content to the top.

## Required shared primitives

Use `Button`, `Input`, `Select`, `Badge`, `Table`, `Dialog`, `StatePanel`,
`Skeleton`, and `Toast` for their respective roles. If a missing capability is
needed, extend the primitive first so every screen receives the same behavior.

## Release check

Before shipping a UI change, inspect at least a dashboard/detail or list/form
pair, one dialog, the admin page when affected, and the mobile drawer. Run
typecheck, lint, component tests, a production build, an axe WCAG scan, and a
horizontal-overflow check at 390px.
