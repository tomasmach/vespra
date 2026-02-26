# Vespra

## What This Is

Vespra is a Discord AI companion with persistent memory and a web management UI. It supports multiple bots, per-channel response modes, and tool-based interactions. This project focuses on fixing the smart response system, which currently causes the bot to stay completely silent even when directly addressed.

## Core Value

The bot must feel like a genuine Discord participant — always responding when addressed, and occasionally chiming in on its own without being too noisy.

## Requirements

### Validated

- ✓ Multi-bot Discord support with per-agent tokens — existing
- ✓ SQLite-backed persistent memory (save/recall/forget) — existing
- ✓ Response modes: smart, mention, all, none — existing
- ✓ Tool system: reply, react, memory_save, memory_recall, web_search — existing
- ✓ DM support (always routes to default bot session) — existing
- ✓ Web management UI for config and memories — existing
- ✓ Hot-reload of config without restart — existing

### Active

- [ ] @mention always triggers a guaranteed reply (no LLM silence decision)
- [ ] DM always triggers a guaranteed reply (confirm the fix works end-to-end)
- [ ] Bot's display name in message text triggers a reply (e.g. "hey Vespra, ...")
- [ ] Smart mode occasionally chimes in on active conversations — not too frequently, not silent
- [ ] Smart mode prompt gives the LLM concrete guidance on when to respond vs. stay quiet

### Out of Scope

- Fully autonomous ambient messages with no user activity — would require timers/crons and is too risky for spam
- Rewriting the LLM or tool infrastructure — existing system works fine
- Overhauling the soul/personality format — out of scope for this fix

## Context

- Default response mode is `smart` (config/config.go:168)
- `isAddressedToBot()` only detects `<@ID>` mentions and message replies — not name-in-text
- In smart mode, even when addressed, the LLM receives "only respond when genuinely warranted" — no guarantee
- Plain-text LLM output in smart mode is suppressed; bot must use the `reply` tool (agent.go:908)
- Smart mode prompt gives no criteria for what "genuinely warrants" means, causing the LLM to default to silence
- Soul file exists but is nearly empty — personality guidance is minimal

## Constraints

- **Go + discordgo**: All changes must stay within the existing Go architecture
- **No breaking changes**: Response modes (smart/mention/all/none) must continue to work as configured
- **Not too spammy**: Smart mode chime-ins should feel natural, not flood channels

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|----------|
| Guarantee reply when addressed | DMs and @mentions should never be ignored — that's the minimum expectation | — Pending |
| Name-in-text detection | Users type bot names without @; missing this makes the bot feel unresponsive | — Pending |
| Improve smart prompt rather than removing it | Smart mode's selective behavior is the feature — just needs better calibration | — Pending |

---
*Last updated: 2026-02-26 after initialization*
