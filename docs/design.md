# Sweef design

Fast, calm, confident, a bit playful. Paper-white and deep-ink surfaces, one electric accent (Volt `#5B5BFF`), mint `#3DF5C4` only for the agent working state and speed moments. Previews of every screen: `docs/design/*.png` (regenerate: `go run ./internal/ui/cmd/uipreview -out docs/design`).

## Personas
- **Power user (Dana, dev).** Lives in the keyboard. Wants Ctrl+K, tab search, a speed readout she can trust, an agent that shows exact actions. Fear: a toy.
- **Privacy-minded (Sam).** Reads the permission popover. Wants per-site agent visibility, honest incognito copy, no hidden uploads. Fear: marketing claims.
- **Busy professional (Priya).** Wants tasks done: "find, compare, fill, add to cart, stop before paying." Fear: an agent that spends money.
- **Student (Leo).** Wants summaries, explanations, fact-checks on whatever page is open. Fear: cost and complexity (needs a key).

## Emotional hook and funnel
Hook: the window feels instant and calm, and the first thing you see is a pill that says "Ask or type a URL". Conversion path: install -> first run (3 steps, always skippable, import + theme + default) -> watch the scripted Sidekick demo -> paste key (stays on device) -> new tab with prompt rows -> first agent success in under 60 s. Every step shows one next action; the key step explains storage in plain words and has "I will do this later"; the new tab prompt rows (including an Agent mode row) are the first success. Measure: time from first run to first completed agent run, key-skip rate, visibility default chosen.

## Principles
1. Honest: no claim without a measurement; alpha is stated; sites that may break get a banner with "Open in system browser"; the speed readout is measured locally and never invented; the stats card renders only if the app provides numbers.
2. The user holds the wheel: Take control and Stop are always visible during runs; sensitive actions show the exact action and need Approve; page visibility is one click per site.
3. Light: system fonts, inline SVG, no network, small DOM, transform/opacity animations only. The chrome page loads about 134 KB of JS+CSS (budget 150 KB; it was about 122 KB before the Data Saver chip and its copy); the Sidekick (about 40 KB), the palette and the data popover load on first use.
4. One accent. Generous radii (10/14/999), 1px hairlines, soft layered shadows, tabular numerals for stats.
5. Calm motion that respects `prefers-reduced-motion`.

## Components
Tab strip (pinned, audio, spinner, drag-reorder, overflow scroll, tab search), toolbar, omnibox with ghost completion and sections, site-info popover with agent-visibility, main menu, downloads popover, find bar, toasts, compat banner, bookmarks bar, command palette, Sidekick (header, page chip, messages, thinking, step timeline, confirm card, sources, error and rate-limit states, empty state with page-type starters, composer, latency strip, resizer), agent overlay (glow frame, cursor, pill, You are in control), new tab (and incognito), onboarding, settings, history, bookmarks, downloads, error pages, and the Data Saver set: meter chip, data popover, Settings -> Data, hard-stop page, onboarding data-plan step, new-tab data line, Sidekick research line. Tokens: `web/shared/tokens.css` (light, dark, incognito); primitives: `base.css`. See `docs/design/design-system.png`.

## Copy deck
All strings live in `web/shared/strings.js`; `S.brand` renames the product and assistant in one place (`{brand}`, `{agent}` placeholders). Voice: short sentences, plain verbs, no hype, say what will happen ("Sidekick can read this site when you ask it to"). Errors say what is wrong and what to do. Refusals and limits are never blamed on the user.

## Data Saver design notes
For people on small data plans (a 100 MB plan should feel generous). Calm, plain, never guilt-trippy: it says what is happening and what the person can do; no exclamation marks, no red until the budget is actually used up, no nagging (each threshold speaks once per period).
- **Meter chip** (toolbar, between the address bar and the Sidekick button): drop icon and "39 MB / 100 MB"; the pill itself fills left to right as the budget is used (no ring, a small ring reads as a spinner). Neutral until the first warn threshold (70%), amber from there, firmer amber from 90% and once the budget is used up (never an error red: using your data is not an error). Hidden when the meter is off, when no budget and no saver level is set, and when the engine cannot count (no fake zeros).
- **Popover**: ring with "61 MB left" in the middle, level switch (Off / Standard / Text first), "This page" with three rows: *Used on this page* (Measured), *Saved by cache and limits* (Measured), *N requests not loaded, about 1.9 MB* (Estimate). Measured is a filled green pill, Estimate an outlined grey pill, the estimate row is lighter, and the legend says estimates are never added to measured numbers. Then "Load images on this page (n)", "Load fully, once", the per-site rule (Auto / Always full / Always lite) and the five busiest sites (approximate). Private windows show no site list: it would reveal what normal windows did.
- **Settings -> Data**: three level cards that say what each level does and does not do in plain language, a budget editor (presets 100 MB / 500 MB / 1 GB / No limit, custom size, period, reset day, hard stop off by default), "So far this month" with the same measured / estimate split, per-site rules, light loading for the Sidekick, show the meter. A closing note says what is not counted (the Sidekick's own messages to the model). Copy lists only real savings: this engine loads no web fonts and renders no iframes in any mode, so those are never listed.
- **Hard-stop page** (`sweef://datastop`): one calm card, three choices (Load this page once, Raise limit with presets, Wait until reset), and a line saying loading once still counts.
- **Onboarding** step 3: "Do you have a data plan?" with four cards; skippable ("I am not sure" or Continue); says which level it started in and that it can change.
- **New tab**: one line under the search pill, "61 MB left this month"; only when a budget is set.
- **Sidekick**: after a research run, "Research used 412 KB" from the agent tabs' real counters, with the estimate (if any) on a second muted line.
- Placeholders for withheld images are drawn by the engine; the UI's part is the copy ("Tap to load" in the strings) and the page-level button.

## Accessibility
Roles: tablist/tab with roving tabindex and arrow keys, toolbar, combobox/listbox for the omnibox and palette, dialog for popovers, log for the chat, status for toasts and the overlay pill, radiogroup for choices, switch for toggles. Visible focus ring everywhere (`:focus-visible`). Esc closes popovers, palette, find, suggestions. Contrast: text tokens meet 4.5:1 on their surfaces in all three themes (checked by eye on previews; run a contrast audit before beta). Reduced motion collapses all durations. Known gap: the voice button is a disabled placeholder; screen-reader testing on a real shell is still to do.

## Next features, ranked
1. **Per-site agent permissions** (read, act, never) with an audit log: trust is the product.
2. **Workflows from agent runs** (record, name, replay, edit steps): turns one success into a habit.
3. **Agent memory** (opt-in, local, viewable and deletable): makes the second run better than the first.
4. **Tracker and ad blocking dashboard** with honest counts: a tangible privacy proof.
5. **Cross-tab research** (select tabs, get a synthesis with sources): the killer student/pro use case.
6. **Scheduled agents** (daily digest, price watch): reasons to open the browser.
7. **Tab groups with AI auto-naming** and **vertical tabs**: scale for power users.
8. **Split view**, **reader mode**, **page-to-notes**: daily utility, cheap with our engine.
9. **Focus mode** (mute tabs, hide the Sidekick, block distractions).
10. **Voice** (push to talk): after the above are solid.
