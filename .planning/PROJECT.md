# Vespra

## What This Is

Vespra is a Discord AI companion with persistent memory and a web management UI. It supports multiple bots, per-channel response modes, and tool-based interactions. The smart response system now guarantees replies when directly addressed and participates naturally in active conversations via smart mode.

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
- ✓ @mention always triggers a guaranteed reply (no LLM silence decision) — v1.0
- ✓ DM always triggers a guaranteed reply (end-to-end confirmed) — v1.0
- ✓ Bot's display name in message text triggers a reply (case-insensitive, e.g. "hey Vespra, ...") — v1.0
- ✓ Smart mode occasionally chimes in on active conversations — not too frequently, not silent — v1.0
- ✓ Smart mode prompt gives the LLM concrete guidance on when to respond vs. stay quiet — v1.0
- ✓ Smart mode chattiness is tunable via soul file personality adjectives — v1.0

### Active

(None — planning next milestone)

### Out of Scope

- Fully autonomous ambient messages with no user activity — would require timers/crons and is too risky for spam
- Rewriting the LLM or tool infrastructure — existing system works fine
- Overhauling the soul/personality format — out of scope

## Context

- Shipped v1.0 Smart Responding with ~8,100 LOC Go total; 157 net lines added for milestone
- Tech stack: Go, discordgo, SQLite (go-sqlite3), OpenRouter API
- Smart mode response guarantee implemented via `addressed bool` propagation through `buildSystemPrompt`
- Name-in-text detection uses case-insensitive substring match (`strings.ToLower`)
- Soul file Smart Mode section documents tuning levers for operators
- All tests pass (go test ./...)

## Constraints

- **Go + discordgo**: All changes must stay within the existing Go architecture
- **No breaking changes**: Response modes (smart/mention/all/none) must continue to work as configured
- **Not too spammy**: Smart mode chime-ins should feel natural, not flood channels

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|----------|
| Guarantee reply when addressed | DMs and @mentions should never be ignored — minimum expectation | ✓ Good — works via system prompt injection |
| Name-in-text detection | Users type bot names without @; missing this makes the bot feel unresponsive | ✓ Good — case-insensitive substring match |
| Improve smart prompt rather than removing it | Smart mode's selective behavior is the feature — just needs better calibration | ✓ Good — example-driven prompt with frequency anchor |
| Example-driven prompt design | Explicit scenario lists give the LLM clear classification heuristics vs. vague "genuinely warrants" | ✓ Good — concrete respond/stay-quiet lists |
| "1 in 5" frequency anchor | Gives LLM a calibration baseline for occasional participation | ✓ Good — matches intended smart mode behavior |
| Embed tuning guide in defaultSoul | LLM reads soul text before smart-mode instruction; personality adjectives correctly interpreted | ✓ Good — soul-level tuning works without code changes |
| Break after reply tool fires | Reply is terminal — re-prompting LLM after replying caused 4-5 duplicate messages | ✓ Good — loop eliminated |
| Exempt addressed turns from suppression guard | Addressed turns must never be silently dropped even if LLM uses plain text instead of reply tool | ✓ Good — @mention/DM always reaches user |
| addressed bool via turnParams | Computed at handler entry, threaded through — avoids re-deriving inside processTurn | ✓ Good — clean propagation pattern |

---
*Last updated: 2026-02-26 after v1.0 milestone*
