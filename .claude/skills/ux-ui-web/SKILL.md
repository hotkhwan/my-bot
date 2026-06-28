---
name: ux-ui-web
description: ANNY web/Mini-App UX standards. Use on EVERY change to internal/dashboard/dist/index.html.
---

# ANNY Web UX/UI — apply on every dashboard change

The dashboard is a single self-contained `internal/dashboard/dist/index.html` (vanilla JS + CSS,
no build) rendered in desktop browsers AND the **Telegram Mini App webview** (iOS/Android).
Webviews are the strict environment — design for them.

## Rules

1. **No overflowing / wrapping labels.** A field label is short (1–3 words). Put any explanation
   behind an **info badge** `ⓘ` (`.ibadge` + `showToast(data-info)`), not inline text that wraps.
   Apply this pattern wherever you'd otherwise write a long hint.
2. **Mobile-webview-safe controls only.** `<datalist>` is unreliable in webviews (can't open/pick)
   — do NOT use it for a picker. For "type-or-pick", use the **custom combobox** (text input +
   `▾` caret + an absolutely-positioned `.combo-menu` of clickable items). Native `<select>` is
   fine for fixed lists. One visible field per input — never two controls for the same value.
3. **Hide empty containers.** Output/console boxes start `hidden` and reveal only when they have
   content (e.g. `#cmd-out`).
4. **Tap targets ≥ ~32px**, bottom nav clears the safe-area; keep a footer disclaimer.
5. **Realtime cadence scales with tier** (see `pollMs()` from `me.tier`): Crew(free) 8s ·
   Captain 3s · Commander 1s. Always `clearInterval` on view change.
6. **Risk-first wording** (see `docs/branding/positioning.md`): "AI Assessment · Confidence ·
   Suggested Action", never "AI says BUY". Lead with risk/transparency, not profit.
7. **Verify after every edit:** the JS-syntax + `$("id")`-resolution node check (see
   `bot-qa-validation`), then Playwright. No undefined ids, no JS errors.

## Reusable bits already in index.html

- `showToast(msg)` + `.ibadge[data-info]` → info badges.
- `.combo` / `.combo-menu` → the symbol combobox.
- `pollMs()` → tier-based poll interval.
