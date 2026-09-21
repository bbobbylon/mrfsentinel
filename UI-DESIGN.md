# UI/UX Design Documentation

**Project:** MRF Sentinel
**Date:** 2026-09-20
**Scope:** The server-rendered interface in `internal/web/templates/` and `internal/web/static/`.

Related: [SRS.md](SRS.md) (what the screens must do), [ARCHITECTURE.md](ARCHITECTURE.md) (why there
is no SPA), [AUTH.md](AUTH.md) (the sign-in flow these screens wrap).

---

## 0. The premise

The source proposal for this product described the UI need as **"a report, not a beautiful
experience."** That line drove every decision below, and it is worth stating up front because it
explains the omissions as much as the inclusions.

The interface is five server-rendered pages, one stylesheet, and one 35-line JavaScript file. There
is no component framework, no CSS framework, no design-token pipeline, no build step, and no
`node_modules`. The whole frontend is compiled into the Go binary via `go:embed` (see
`internal/web/embed.go`), which is what makes "deploy the frontend" a step that does not exist.

The audience is a compliance officer who needs to read a verdict and act on it. Density, legibility,
and unambiguous status are the design goals. Delight is not one.

---

## 1. Design System

Everything is defined as CSS custom properties on `:root` in
[internal/web/static/style.css](internal/web/static/style.css). There is exactly one place to change
a color.

### 1.1 Color palette

| Token | Value | Role |
|---|---|---|
| `--ink` | `#1a1f2b` | Primary text. Near-black with a blue cast, softer than pure `#000`. |
| `--muted` | `#5b6472` | Secondary text — detail columns, timestamps, hints. |
| `--border` | `#dde1e7` | Card borders, table rules, input outlines. |
| `--bg` | `#f7f8fa` | Page background and table header fill. |
| `--card-bg` | `#ffffff` | Cards, tables, the top bar. |
| `--brand` | `#2c5cc5` | Links, primary buttons. The only chromatic accent that is not a status. |

**Status colors.** Each is a foreground/background pair, used only as a pair:

| Status | Foreground | Background | Meaning |
|---|---|---|---|
| Pass | `--pass` `#1b7a3d` | `--pass-bg` `#e6f5ea` | Rule satisfied; file compliant. |
| Fail | `--fail` `#b3261e` | `--fail-bg` `#fbe9e8` | Rule violated; also reused for hard errors. |
| Pending | `--pending` `#855c00` | `--pending-bg` `#fdf1d6` | Check queued or running. |
| Neutral | `--muted` `#5b6472` | `--muted-badge-bg` `#eceff2` | Never checked — absence of a verdict, not a bad one. |

The palette is deliberately three-signal (green / red / amber) plus one neutral. A compliance report
has no fourth status, so there is no fourth color.

**Known gap: no dark mode.** There is no `prefers-color-scheme` block and no `[data-theme]` hook.
Every value above is a light-mode value. Adding dark mode would be a contained change — redefine the
tokens inside a media query — but it has not been done.

### 1.2 Typography

| | |
|---|---|
| Body | `-apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif` |
| Monospace | `ui-monospace, SFMono-Regular, Menlo, Consolas, monospace` at `0.9em`, via `.mono` |
| Base line-height | `1.5` |
| `h1` | `1.6rem` |
| `h2` | `1.15rem`, `2rem` top margin |
| Table headers | `0.8rem`, uppercase, `0.03em` letter-spacing, `--muted` |
| Small text | `0.85rem`–`0.9rem` (badges, hints, sampled failures) |

A system font stack rather than a webfont: no network request, no layout shift, no font-loading
strategy, and the result is already correct on every platform. Monospace is applied only where
character-level precision matters — MRF URLs and the detected format string.

### 1.3 Spacing and shape

Spacing is `rem`-based and informal rather than a strict scale: `0.2rem` through `2rem`, with
`0.5rem`/`0.65rem`/`0.9rem`/`1.25rem`/`1.5rem` carrying most of the weight. Corner radii are
`6px` (inputs, buttons), `10px` (cards, tables), and `999px` (badge pills). Borders are a uniform
`1px solid var(--border)`.

### 1.4 Iconography

None. No icon font, no SVG sprite, no emoji. Status is conveyed by a text label inside a colored
pill, which is both simpler and more accessible than an icon (see §5.2).

---

## 2. Component Library

All components are plain CSS classes in `style.css`. There is no component abstraction layer — a
"component" here is a class name plus the markup convention for using it.

| Class | Purpose | Used in |
|---|---|---|
| `.topbar` | Fixed-height site header: brand, tagline, and the signed-in user's email with a sign-out button. Renders the user block only when someone is signed in. | `layout.html` |
| `.container` | Centered content column, `max-width: 960px`. | `layout.html` |
| `.card` | White panel with border and radius. `.card.narrow` caps it at `420px` for forms. | login, check-email, dashboard, run |
| `.banner` | A `.card` tinted by status, used for the headline verdict at the top of a report. Variants: `.banner-pass`, `.banner-fail`, `.banner-pending`, `.banner-error`. | `run.html` |
| `.badge` | Status pill. Variants: `.badge-pass`, `.badge-fail`, `.badge-pending`, `.badge-error`, `.badge-muted`. | dashboard, hospital, run |
| `.report-table` | The workhorse: full-width, collapsed borders, tinted uppercase header row, top-aligned cells. Every list in the app is one of these. | dashboard, hospital, run |
| `button` | Primary action — brand fill, white text, left-aligned rather than stretched. | all forms |
| `.link-button` | A `<button>` styled as a link, so sign-out can be a real `POST` form rather than a `GET` link. | `layout.html` |
| `.sample-list` | Compact `<ul>` of sampled failing rows inside a table cell. | `run.html` |
| `.mono`, `.muted`, `.truncate`, `.error` | Utilities: monospace, secondary text, single-line ellipsis (`max-width: 320px`), and inline form errors. | throughout |

### 2.1 Template structure

`layout.html` is the shell, providing two blocks:

- `{{block "title"}}` — per-page `<title>`, defaulting to `MRF Sentinel`.
- `{{block "content"}}` — the page body.

Each of the five pages defines both. Note that every page is parsed into its **own**
`*template.Template` rather than one shared set — otherwise each page's `define "content"` would
collide, since `define` names share one namespace per template. The reasoning is documented on
`pageFiles` in [internal/web/templates.go](internal/web/templates.go).

Two template functions are available (`templateFuncs`, same file):

- `percent` — formats a `0.0`–`1.0` rate as `87%`. `html/template` has no arithmetic, so any
  computation happens in Go first.
- `derefBool` — **safety-critical.** `{{if}}` on a `*bool` only tests non-nil; it does not
  dereference. Every branch on `OverallPassed` (nil while a run is in progress) must use this. A
  bare `{{if}}` here once showed "Compliant" for runs that had actually failed — see
  ARCHITECTURE.md.

---

## 3. Layout Patterns

### 3.1 Page structure

```
┌──────────────────────────────────────────────────────────┐
│ MRF Sentinel   CY2026 …checklist      user@x.org [Sign out] │  .topbar
├──────────────────────────────────────────────────────────┤
│                                                          │
│    ┌──────────────── .container (max 960px) ─────────┐   │
│    │  h1                                             │   │
│    │  [ .banner — verdict, on report pages ]         │   │
│    │  h2                                             │   │
│    │  ┌─── .report-table ──────────────────────┐     │   │
│    │  │ RULE        │ RESULT │ DETAIL          │     │   │
│    │  │ …           │ [badge]│ …               │     │   │
│    │  └────────────────────────────────────────┘     │   │
│    │  [ .card — form, where the page has one ]       │   │
│    └─────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────┘
```

One column, always. No sidebar, no nav tree, no modals, no tabs. Navigation is the brand link back
to the dashboard plus an explicit "← back" link on nested pages.

### 3.2 Responsive behavior

A single breakpoint, at **640px**:

- `.topbar` wraps rather than compressing.
- `.report-table` becomes `display: block; overflow-x: auto` — horizontal scroll instead of a card
  reflow. A compliance table's columns are meaningful in relation to each other, so scrolling
  preserves more than stacking would.

Everything else is fluid: `max-width: 960px` with `1.5rem` gutters handles the rest. There is no
tablet-specific treatment and no mobile-first authoring — the desktop layout degrades, which is the
right investment for a tool used at a desk.

### 3.3 Navigation map

```
/                    → redirect → /dashboard
/login               ⇄ /login (POST) → "check your email"
/auth/callback?token → /dashboard
/dashboard           → /hospitals/{id} → /runs/{id}
                     └ POST /hospitals → /hospitals/{id}
/hospitals/{id}      └ POST /hospitals/{id}/runs → /runs/{id}
/runs/{id}           ← polls /runs/{id}/status while in flight
POST /logout         → /login
```

Every state-changing action is a `POST` that redirects (303 See Other), so no page reachable by
refresh or back-button re-submits anything.

---

## 4. User Flows

### 4.1 Sign in

```
/login ──POST email──► "Check your email" ──(email)──► /auth/callback?token ──► /dashboard
   ▲                                                          │
   └──────────── invalid / expired / already-used ─────────────┘
                 (re-renders /login with a message)
```

- The confirmation page is identical whether or not the address has an account (FR-1.2).
- It links to Mailhog at `localhost:8025` for local development, so the link is readable without a
  real mail provider.
- An invalid address re-renders the form at **422** with the message inline — no redirect, no lost
  input.

### 4.2 Add a hospital and get a first verdict

```
/dashboard ──name + MRF URL──► POST /hospitals ──► run created ──► /hospitals/{id}
                    │                                                   │
                    └── invalid (422, values preserved)                 └─► "Run a new check" → /runs/{id}
```

The form's submit button reads **"Add and check now"**, and adding really does start a run
immediately (FR-2.2) — the officer never has to know that "add" and "check" are separate ideas.

### 4.3 Watch a check finish

```
/runs/{id} ──in flight──► pending banner + app.js poller
                              │  GET /runs/{id}/status every 3s (5s after an error)
                              ▼
                          status leaves pending/running → window.location.reload()
                              ▼
                          full report
```

`app.js` is included by `run.html` **only** while the run is in flight, and reads its polling URL
from a `data-run-status-url` attribute on its own `<script>` tag. When the run is finished, the
script is not on the page at all.

### 4.4 Read a report

Top to bottom, in the order a person needs it:

1. **Verdict banner** — "Every checklist rule passed", "This file has one or more compliance issues",
   "This check couldn't complete", or the in-flight notice.
2. **Partial-parse warning**, if the file stopped parsing — stating plainly that results cover the
   rows read before that point, and how many.
3. **Context line** — detected format and rows checked.
4. **Hospital-level checks** — rule, pass/fail badge, and detail text explaining *why*.
5. **Item-level checks** — rule, compliance-rate badge, rows checked, rows failed, and up to 20
   sampled failing rows.

The compliance rate is shown as a percentage in a pass- or fail-colored badge, which is the
distinction the whole report exists to make: `0%` (a column is missing everywhere) and `98%` (a few
bad rows) are different problems needing different work.

---

## 5. Accessibility

**Target: WCAG 2.1 Level AA.** Current state is close, with one measured failure listed below.

### 5.1 What is in place

- `<html lang="en">` and a responsive viewport meta tag.
- Every form input has an explicitly associated `<label for=…>` matching the input's `id`.
- Semantic structure throughout: real `<table>`/`<thead>`/`<th>`, one `<h1>` per page, `<main>`,
  `<header>`, `<form>`.
- Sign-out is a real `POST` form with a `<button>` (styled via `.link-button`), so it is keyboard-
  operable and correctly announced — not a link doing a state change.
- Keyboard navigation works by construction: there are no custom widgets, no `tabindex`
  manipulation, and no JavaScript-driven focus. Everything interactive is a native control.
- External links carry `rel="noopener"`.
- `autofocus` on the email field puts the caret where the user is going anyway.

### 5.2 Status is never color alone

Every badge carries a text label — "Compliant", "Issues found", "Checking…", "Error", "Never
checked", "Pass", "Fail", or a literal percentage. Color reinforces the label; it never *is* the
information. This satisfies WCAG 1.4.1 (Use of Color) and is the single most important accessibility
property of a report whose entire purpose is pass/fail.

### 5.3 Measured contrast ratios

Computed from the tokens in §1.1. AA requires **4.5:1** for normal text and **3:1** for large text
(≥18.66px bold or ≥24px). Badge text is `0.8rem` ≈ 12.8px, so **normal-text thresholds apply to
badges**.

| Foreground | Background | Ratio | AA |
|---|---|---|---|
| `--ink` `#1a1f2b` | `--card-bg` `#ffffff` | 16.47:1 | ✅ AAA |
| `--ink` `#1a1f2b` | `--bg` `#f7f8fa` | 15.50:1 | ✅ AAA |
| `--brand` `#2c5cc5` | `#ffffff` | 6.10:1 | ✅ AA |
| `#ffffff` | `--brand` `#2c5cc5` (button) | 6.10:1 | ✅ AA |
| `--muted` `#5b6472` | `#ffffff` | 5.98:1 | ✅ AA |
| `--muted` `#5b6472` | `--bg` `#f7f8fa` | 5.63:1 | ✅ AA |
| `--fail` `#b3261e` | `--fail-bg` `#fbe9e8` | 5.58:1 | ✅ AA |
| `--muted` `#5b6472` | `--muted-badge-bg` `#eceff2` | 5.18:1 | ✅ AA |
| `--pass` `#1b7a3d` | `--pass-bg` `#e6f5ea` | 4.78:1 | ✅ AA |
| `--pending` `#855c00` | `--pending-bg` `#fdf1d6` | 5.31:1 | ✅ AA |

Every pair clears AA. That was not true when this document was first written: `--pending` was
`#9a6b00`, which is **4.18:1** on `--pending-bg` — short of the 4.5:1 that the pending badge
("Checking…") needs at 12.8px, and the only failure in the table. It was darkened to `#855c00`,
which is the value shipping now.

If a lighter amber is ever wanted, `#8a5f00` (5.04:1) is the lightest that still clears the bar;
anything above it fails again. The badge text is small, so the 3:1 large-text allowance does not
apply here.

Ratios were computed from the WCAG 2.1 relative-luminance formula against the tokens as they appear
in `style.css`, not sampled from a rendered page — so they hold as long as badges keep using their
paired background and nothing introduces opacity.

### 5.4 Known gaps

Stated rather than glossed:

1. **No skip-to-content link.** The top bar is short, so the cost is low, but it is missing.
2. **No `aria-live` on the in-flight banner.** When a run finishes, `app.js` calls
   `window.location.reload()`; a screen-reader user gets a full page load with no announcement that
   something changed. An `aria-live="polite"` region, or announcing before reloading, would fix it.
3. **`<th>` elements have no `scope` attribute.** The tables are simple enough that most screen
   readers infer correctly, but `scope="col"` should be explicit.
4. **`.truncate` hides content with no recovery.** Long MRF URLs are ellipsized at 320px with no
   `title` attribute and no expand affordance. The link still resolves, but the text is unreadable.
5. **No visible focus styling beyond the browser default.** Defaults are adequate on current
   browsers, but a deliberate `:focus-visible` treatment would be better on the brand-colored button.
6. **Never tested with an actual screen reader.** The claims above are derived from the markup and
   from computed ratios, not from a NVDA/JAWS/VoiceOver session. Treat §5.1 as "structurally sound,"
   not "verified assistive-technology support."

---

## 6. Styling Conventions

- **One stylesheet**, `internal/web/static/style.css`, served from the embedded filesystem at
  `/static/style.css`. No preprocessor, no bundler, no minification, no CSS-in-JS.
- **No CSS framework.** A handful of screens does not earn a framework's payload or its build step —
  and skipping it is what leaves the frontend with no build step *at all*, which is the property that
  makes `go:embed` sufficient.
- **Plain descriptive class names**, not BEM or utility classes: `.report-table`, `.badge-pass`,
  `.card.narrow`. Modifiers are a second class alongside a base class.
- **Custom properties for every color.** No hex literal appears outside `:root` except `#fff` on the
  primary button.
- **Element selectors for form controls** (`button`, `form input`, `form label`) rather than classes,
  since the app's forms are uniform and there is no third-party markup to collide with.
- **Mobile adjustments last**, in one `@media (max-width: 640px)` block at the end of the file.
- **JavaScript is a last resort.** The only script is the run-status poller, written as a plain IIFE
  in ES5-compatible syntax with `"use strict"`, no dependencies, and no build step. Every other
  interaction in the app is a form submission or a link.

---

## 7. Mockups and References

There are none — no Figma file, no Adobe XD document, no wireframes. The interface was designed
directly in `style.css` and the templates, which for five pages of tables and badges was the shorter
path to the same result.

**The templates are the design reference.** To see what a screen looks like, read its template
alongside `style.css`, or run the app (`./run.sh` or `run.cmd`) and open
[http://localhost:8080](http://localhost:8080).

| Screen | Template |
|---|---|
| Shell — top bar, container | [internal/web/templates/layout.html](internal/web/templates/layout.html) |
| Sign in | [internal/web/templates/login.html](internal/web/templates/login.html) |
| Check your email | [internal/web/templates/check_email.html](internal/web/templates/check_email.html) |
| Dashboard | [internal/web/templates/dashboard.html](internal/web/templates/dashboard.html) |
| Hospital detail and history | [internal/web/templates/hospital.html](internal/web/templates/hospital.html) |
| Compliance report | [internal/web/templates/run.html](internal/web/templates/run.html) |

If the UI ever grows past "a report" — bulk operations, charts over time, anything genuinely
interactive — that is the point to revisit both this document and the no-SPA decision in
ARCHITECTURE.md. Neither was chosen to avoid a frontend framework; both were chosen because this
shape of UI does not need one.
