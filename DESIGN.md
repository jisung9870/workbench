# Design

## Source of truth

- Status: Active
- Last refreshed: 2026-08-05
- Primary product surfaces: loopback Dashboard (`/`) and embedded documentation site (`/guide`)
- Evidence reviewed: `README.md`, `docs/*.md`, `internal/dashboard/assets/*`,
  `internal/dashboard/dashboard.go`, `internal/dashboard/dashboard_test.go`, and the parent architecture plans in
  `../plan/`

## Brand

- Personality: calm, precise, local-first, operator-oriented
- Trust signals: explicit ownership, visible health, typed actions, recovery guidance, and no hidden remote service
- Avoid: decorative observability charts without real data, ambiguous destructive actions, excessive gradients, and
  product claims that exceed implemented behavior

## Product goals

- Goals: make local projects, Agent tasks, worktrees, Git state, and capability health understandable at a glance;
  make the system learnable without reading source code; support dark, light, and system themes
- Non-goals: replace the CLI, become a daemon, expose Workbench remotely, or add arbitrary browser-side commands
- Success signals: a new operator can install, open a project, start an Agent, interpret Doctor, and recover from a
  common failure using only the embedded guide

## Personas and jobs

- Primary personas: the workstation owner, a returning operator after context loss, and a contributor changing one
  Workbench component
- User jobs: understand the architecture, complete routine workflows, inspect ownership and health, troubleshoot a
  failed backend, and verify safe recovery
- Key contexts of use: macOS with cmux, Linux/WSL with tmux, native Windows with Windows Terminal, and headless CLI use

## Information architecture

- Primary navigation: Dashboard, Guide, source repository
- Core routes/screens: `/` operational Dashboard; `/guide` overview, quickstart, concepts, guides, reference,
  operations, security, troubleshooting, and glossary
- Content hierarchy: product definition and quickstart first; mental model before procedures; exact reference after
  task guides; recovery and limitations near every risky boundary

## Design principles

- Local state stays legible: identify which component owns each datum and which surfaces only consume it.
- Progressive disclosure: summarize health and activity first, then reveal exact paths, commands, states, and recovery.
- Safety is part of the interface: disabled, unsupported, unavailable, and destructive states must be distinct.
- Tradeoffs: prioritize dense operator information over marketing whitespace, while retaining readable line length and
  responsive navigation.

## Visual language

- Color: neutral graphite dark theme and warm paper light theme; lime is the primary/action color, blue is
  informational, amber is warning, and red is destructive/error
- Typography: system sans-serif for interface and prose; system monospace for commands, paths, IDs, and schema fields
- Spacing/layout rhythm: 4/8 px base rhythm; compact controls; 16–24 px content spacing; documentation prose capped at
  a readable measure
- Shape/radius/elevation: 9–16 px radii, one-pixel semantic borders, restrained shadows only for floating notices and
  sticky navigation
- Motion: short color/border transitions only; no required animation
- Imagery/iconography: typography, small status marks, CSS diagrams, and sanitized real-product screenshots when they
  explain spatial roles or a workflow; no decorative or invented interface illustrations

## Components

- Existing components to reuse: top bar, brand mark, project list, health card, metric cards, panels, rows, Agent
  cards, status pills, notices, and typed action buttons
- New/changed components: theme switch, product navigation, guide sidebar, search, section cards, callouts, command
  blocks, architecture flow, product screenshot figure/caption, feature matrix, and page table of contents
- Variants and states: dark/light/system theme; active/hover/focus/disabled controls; info/warning/danger callouts;
  available/unavailable/skipped health states
- Token/component ownership: `internal/dashboard/assets/style.css` owns shared CSS variables and components;
  `theme.js` owns the device-local theme preference

## Accessibility

- Target standard: WCAG 2.2 AA for color contrast, keyboard use, labels, and document structure
- Keyboard/focus behavior: visible focus rings, skip links, native buttons/links, searchable navigation, and no
  hover-only information
- Contrast/readability: theme-specific semantic tokens; prose measure and line height; status never communicated by
  color alone
- Screen-reader semantics: landmark elements, labelled navigation, live status region, descriptive control labels,
  and heading order
- Reduced motion and sensory considerations: respect `prefers-reduced-motion`; transitions are optional decoration

## Responsive behavior

- Supported breakpoints/devices: modern desktop browsers, tablets, and narrow mobile browser windows
- Layout adaptations: three-column Dashboard collapses to two then one; guide left navigation becomes a horizontal
  scroller and the page TOC collapses below the article
- Touch/hover differences: controls keep at least a practical touch target and all hover affordances have focus states

## Interaction states

- Loading: explicit loading copy and existing snapshot status
- Empty: explain the next action instead of showing an empty frame
- Error: plain-language notice plus actionable recovery where available
- Success: brief non-blocking status notice and refreshed state
- Disabled: state why the action is unavailable through adjacent text or a title
- Offline/slow network: the embedded guide remains available; Dashboard snapshot errors preserve the last rendered page

## Content voice

- Tone: operational, direct, evidence-based, and respectful of platform differences
- Terminology: Workbench, project, Agent, task, worktree, backend, provider, registry, capability, and compatibility
  observation retain their contract meanings
- Microcopy rules: lead with the outcome; name the exact command or state; distinguish required, optional, disabled,
  and unsupported; never imply that advisory observations authorize automatic fallback deletion

## Implementation constraints

- Framework/styling system: Go `embed`, `net/http`, static HTML/CSS/vanilla JavaScript; no frontend build system
- Design-token constraints: extend the existing CSS custom properties instead of introducing another theme layer
- Performance constraints: no remote fonts, images, analytics, or runtime dependencies; sanitized product screenshots
  and all other guide assets ship in the binary
- Compatibility constraints: restrictive same-origin CSP, loopback-only server, no CORS, and device-local theme
  preference only
- Test/screenshot expectations: Go handler contracts, JavaScript syntax checks, targeted DOM/theme assertions, and a
  browser smoke for light/dark persistence and guide navigation

## Open questions

- [ ] Decide whether a future release should publish a versioned public copy; current security and lifecycle contracts
  intentionally keep the guide inside the loopback Dashboard.
- [ ] Add version selection only after Workbench has tagged releases with maintained compatibility documentation.
