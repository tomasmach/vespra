# Codebase Concerns

**Analysis Date:** 2026-02-26

## Tech Debt

**HTTP Client Global Default:**
- Issue: `http.DefaultClient` is used directly in multiple places without custom timeout configuration.
- Files: `llm/llm.go:339`, `tools/web_fetch.go:78`, `web/server_test.go:237`, `web/server_test.go:272`
- Impact: Uses Go's default 0-timeout (blocking forever). While per-tool contexts are set with timeouts, the HTTP client itself could hang if a server keeps the connection open indefinitely without sending data.
- Fix approach: Either create a custom `*http.Client` with default timeouts for global use, or ensure all timeouts are strictly enforced at the context level. The context approach is currently in place but relying solely on it is fragile.

**Incomplete Context Handling in Shutdown:**
- Issue: Main graceful shutdown uses 5-second timeout (`main.go:150`), but router drain waits 30 seconds (`agent/router.go:281`). These timelines may not align.
- Files: `main.go:150`, `agent/router.go:281-283`
- Impact: If an agent takes 20 seconds to finish, the 5-second shutdown context will timeout before the agent completes, leaving the router drain to wait in vain.
- Fix approach: Extend the shutdown context to match the maximum expected drain time (30+ seconds), or reduce both to a consistent value.

**Agent Resource Cleanup on Hot-Load Failure:**
- Issue: `agent/router.go:184-187` attempts to load a memory store during hot-load. If that fails, the agent is silently skipped, but no retry or recovery mechanism exists.
- Files: `agent/router.go:184-187`
- Impact: If a newly configured agent's memory DB cannot be opened (permissions, disk full, corrupt file), the agent will be unavailable until manual restart or config reload.
- Fix approach: Log with higher severity (Error instead of Error+ context), or implement a periodic retry mechanism for failed agents.

**Conversation Pruning Randomness:**
- Issue: `memory/store.go:268` uses 1-in-500 chance to prune conversations table to 10,000 rows max.
- Files: `memory/store.go:268-273`
- Impact: Pruning happens at unpredictable times during high-traffic writes, potentially causing latency spikes on unlucky requests. No guarantee pruning will happen if traffic is low.
- Fix approach: Use a scheduled background job or track write count explicitly rather than random chance.

## Known Bugs

**Image/Video Download Failures Don't Block Processing:**
- Symptom: If Discord media attachments fail to download (network timeout, 403 Forbidden, etc.), the agent logs a warning and continues without the media part. The user sees no indication media was referenced.
- Files: `agent/agent.go:232-234`, `agent/agent.go:243-245`, `agent/agent.go:253-256`, `agent/agent.go:744-746`, `agent/agent.go:759-761`
- Trigger: Send a Discord message with an image attachment, then immediately block that Discord CDN URL (e.g., temporarily down or restricted).
- Workaround: Retry the message or manually alert the agent that media was missing.

**Spam Block Cooldown Cannot Be Lifted During Runtime:**
- Symptom: User is rate-limited and blocked for 1 hour. No way to unblock them except by restarting the bot.
- Files: `agent/router.go:236-270` (spamMap)
- Trigger: User hits 10 messages in 30 seconds, triggering cooldown. Then tries again in 5 minutes (still blocked).
- Workaround: Restart the bot or add a manual admin command to clear spam records.

**Memory Embedding Failures Are Silently Ignored:**
- Symptom: If OpenRouter embedding API fails (`memory/store.go:91-94`), the memory is saved without an embedding vector. Subsequent recall queries fall back to text-only LIKE search.
- Files: `memory/store.go:91-94`
- Trigger: OpenRouter API outage or key expired; memories are saved but unfindable by semantic search.
- Workaround: Manually regenerate embeddings for affected memories (no built-in tool exists).

## Security Considerations

**Secrets in Config File Not Validated:**
- Risk: Config file may be read-protected but `config.Load()` does not verify permissions. If accidentally world-readable, tokens leak.
- Files: `config/config.go:121-125`, `web/server.go:150-159`
- Current mitigation: Relies on OS file permissions. CLAUDE.md suggests `config.toml` should be protected, but no code enforces it.
- Recommendations: Add file permission check after loading config; log warning if file is world-readable. Consider refusing to start if token is visible in process args.

**API Keys Logged in Debug Output:**
- Risk: `llm/llm.go:317` logs the full request body, which includes API keys in Authorization headers.
- Files: `llm/llm.go:317` (slog.Debug level)
- Current mitigation: Only logged at Debug level; production should use Info level. Still a risk in test environments.
- Recommendations: Strip Authorization header before logging. Use a redaction function for sensitive fields.

**Temp Config File Permissions:**
- Risk: `web/server.go:173` writes temp config with default permissions (0o644), possibly world-readable.
- Files: `web/server.go:173`
- Current mitigation: Temp file is immediately renamed to the real config, minimizing exposure window.
- Recommendations: Explicitly use 0o600 permissions when writing temp config to ensure only owner can read.

**No CSRF Protection on Web Config Endpoint:**
- Risk: POST `/api/config` accepts TOML uploads with no CSRF token or origin check.
- Files: `web/server.go:161-198`
- Current mitigation: Web server is typically only accessible locally or behind a reverse proxy.
- Recommendations: Add CSRF token validation or require a custom header if exposed to untrusted networks.

**Memory Search is Case-Sensitive:**
- Risk: SQL LIKE search in `memory/store.go:173-174` is case-sensitive, making queries fragile.
- Files: `memory/store.go:173-174`
- Current mitigation: Semantic embedding search is case-insensitive, so exact text match is a fallback.
- Recommendations: Consider using `COLLATE NOCASE` for case-insensitive LIKE, or document that search is case-sensitive.

## Performance Bottlenecks

**Media Attachment Downloads Block LLM Call:**
- Problem: `agent/agent.go:209-268` downloads all attachments before sending to LLM. Large files (up to 50 MB videos, 2 MB web fetches) can take seconds.
- Files: `agent/agent.go:209-268`, `agent/agent.go:731-240`
- Cause: Downloads happen synchronously in `buildUserMessage()` during message processing, adding latency to every turn with attachments.
- Improvement path: Consider parallel downloads or async validation. Alternatively, send LLM with media URLs first, then fetch on-demand if model requests content.

**All History Reloaded on First Message to Channel:**
- Problem: `agent/agent.go:511-517` calls `backfillHistory()` which fetches messages from Discord API on first turn.
- Files: `agent/agent.go:511-517`, `agent/agent.go:430-459`
- Cause: If `HistoryBackfillLimit` is high (up to 100), this adds 100+ HTTP requests to Discord API per new channel.
- Improvement path: Implement lazy backfill (fetch only older messages if needed) or cache channel history locally across restarts.

**Embeddings Not Incrementally Generated:**
- Problem: `memory/store.go:266-291` generates embeddings for all messages on every turn, not just new memories.
- Files: `agent/agent.go:956-959` (memory extraction interval)
- Cause: No tracking of which memories need re-embedding; UpdateContent() regenerates even if content unchanged.
- Improvement path: Store a content hash and only re-embed if content changes. For new memories, batch embedding requests.

**Conversation Table Unbounded Until Pruning Hits:**
- Problem: `conversations` table grows unbounded until random pruning kicks in (1 in 500).
- Files: `memory/store.go:253-275`
- Cause: Between pruning events, table can grow very large, slowing queries.
- Improvement path: Use an INSERT trigger with DELETE that keeps table capped, or use a VACUUM after pruning.

**Status Poller Runs Every 5 Seconds:**
- Problem: `web/server.go:101-120` broadcasts agent status to all SSE subscribers every 5 seconds.
- Files: `web/server.go:101-120`
- Cause: If many subscribers are connected, this generates constant JSON marshaling and broadcast overhead.
- Improvement path: Only broadcast if state changed, or increase poll interval to 10-15 seconds.

## Fragile Areas

**Tool-Call Loop May Exceed Max Iterations:**
- Files: `agent/agent.go:844-893`
- Why fragile: If LLM keeps returning tool calls (e.g., infinite memory_save loop), loop will hit `MaxToolIterations` and send generic error message. User has no context why their request failed.
- Safe modification: Add a check after each tool call to detect if the same tool is called repeatedly; log a specific warning.
- Test coverage: `agent/agent_test.go` covers basic tool calls but not infinite loops or boundary cases.

**History Sanitization Can Drop Messages:**
- Files: `agent/agent.go:49-59`
- Why fragile: After history trim, `sanitizeHistory()` drops leading non-user messages. If a tool-call chain is interrupted mid-way and truncated, message structure is lost.
- Safe modification: Preserve more context about why messages were dropped; consider storing dropped messages separately for debugging.
- Test coverage: `agent/agent_test.go` covers basic flow but not edge cases around history truncation.

**Agent Goroutine Supervision is Weak:**
- Files: `agent/router.go:124-139` (spawn new agent)
- Why fragile: If agent goroutine panics, it's not caught. The router cleanup code runs, but no panic recovery logs the error.
- Safe modification: Wrap `a.run(agentCtx)` in a defer that recovers panics and logs them.
- Test coverage: No panic recovery tests.

**Memory Store Database Corruption Not Detected:**
- Files: `memory/store.go:65-80`
- Why fragile: If SQLite file is corrupted, `sql.Open()` succeeds but queries later will fail. No integrity check on startup.
- Safe modification: Run `PRAGMA integrity_check;` on startup and fail loudly if corruption detected.
- Test coverage: No corruption simulation tests.

**Vision Model Disabled Silently When Not Configured:**
- Files: `llm/llm.go:190-211` (vision logic)
- Why fragile: If user sends image but vision model is not configured, images are silently stripped with a note to the LLM. No user-facing indication.
- Safe modification: Log a warning when images are stripped; consider returning an error to the agent to notify the user.
- Test coverage: Image stripping is tested but not user notification.

## Scaling Limits

**In-Memory Agent Map Unbounded:**
- Current capacity: Scales with active Discord channels (100-buffer per agent).
- Limit: If 10,000 channels are active simultaneously, memory usage could be ~100 MB per agent, plus shared state. Actual limit depends on goroutine overhead.
- Scaling path: Implement LRU eviction based on idle time (already exists via `IdleTimeoutMinutes`, but manual cleanup via web API might be needed for bursts).

**SQLite Single-Writer Limit:**
- Current capacity: Multiple goroutines can write (via contexts), but SQLite WAL mode serializes writes.
- Limit: High write contention (e.g., 100+ agents saving memories simultaneously) may cause lock waits.
- Scaling path: Partition memories by shard (separate DB per server), or switch to a different backend (PostgreSQL, MySQL) if write throughput becomes the bottleneck.

**Discord API Rate Limits Not Tracked:**
- Current capacity: Bots can send/receive messages freely, but Discord enforces per-channel and per-user limits.
- Limit: If 100+ channels are sending messages simultaneously, Discord rate limit may be hit, causing temporary failures.
- Scaling path: Implement a rate-limiting queue for outgoing messages, or track Discord rate limit headers and backoff accordingly.

**Memory Embeddings Stored as Blob:**
- Current capacity: Each embedding is 1536 floats × 4 bytes = ~6 KB per memory (assuming 1536-dim model).
- Limit: 10,000 memories × 6 KB = 60 MB per server. Not huge, but adds up with multiple servers.
- Scaling path: Use a vector database (e.g., Qdrant, Pinecone) instead of SQLite for semantic search.

## Dependencies at Risk

**discordgo (bwmarrin/discordgo):**
- Risk: Actively maintained but smaller community. Go stdlib APIs (slices, sync) are relied upon. If discordgo API changes, refactoring needed.
- Impact: Message handling, attachment parsing, gateway connection. All core functionality.
- Migration plan: Pin version in go.mod; have a plan to switch to alternative (e.g., diamondburned/arikawa) if needed.

**go-sqlite3 (CGO dependency):**
- Risk: Requires C compiler at build time. SQLite version is embedded; updates require rebuild.
- Impact: Memory persistence, conversation logging. Core feature.
- Migration plan: Consider pure-Go SQLite alternatives (e.g., crawshaw.io/sqlite) if CGO becomes a burden.

**OpenRouter (External LLM provider):**
- Risk: API may change; pricing may increase; service may go down.
- Impact: All LLM calls require OpenRouter. Agent becomes completely non-functional if service is unavailable.
- Migration plan: Config already supports provider/model override. Plan to add fallback provider (e.g., Ollama local) for resilience.

## Missing Critical Features

**No Memory Deduplication:**
- Problem: LLM can save duplicate memories. `memory_save` tool calls `memory_recall` first, but LLM may ignore the result and save anyway.
- Blocks: User can end up with multiple identical memories, wasting storage and confusing searches.

**No Pagination for Long Memories:**
- Problem: `memory/store.go:183` fetches memories up to default 50 rows. No way for UI to fetch the next page without manual query param manipulation.
- Blocks: Users with 1000+ memories cannot easily browse all of them.

**No Scheduled Tasks or Reminders:**
- Problem: Memories can represent goals (e.g., "remind user about birthday"), but there's no scheduled delivery mechanism.
- Blocks: Agent cannot proactively reach out to users.

**No Multi-User Conversation in Shared Channels:**
- Problem: Agent treats all messages in a shared channel as from one conversation stream. No per-user context separation.
- Blocks: In group channels, agent mixes context between different users.

**No Voice/Audio Support:**
- Problem: Discord voice channels are not supported. Only text.
- Blocks: Audio-based interaction unavailable.

## Test Coverage Gaps

**Web API Security Not Tested:**
- What's not tested: CSRF attacks, malformed JSON bodies, oversized payloads to POST endpoints.
- Files: `web/server.go:150-198` (config endpoint), `web/server.go:366-400` (agent CRUD)
- Risk: Malformed requests could crash the web server or leak information.
- Priority: High - API is exposed to external clients.

**Concurrent Goroutine Interaction:**
- What's not tested: Multiple agents in same channel, simultaneous tool calls, race conditions on shared state.
- Files: `agent/router.go:46-56` (agent map under mu), `agent/agent.go:76-79` (atomic fields)
- Risk: Subtle races under high load could cause memory corruption or lost messages.
- Priority: High - Core feature involves heavy concurrency.

**Database Constraint Violations:**
- What's not tested: Foreign key violations, unique constraint failures on memory ID collisions.
- Files: `memory/store.go:90-129`, `memory/store.go:202-241`
- Risk: Corrupt data or silent failures if DB constraints are violated.
- Priority: Medium - Transactions should prevent most issues, but edge cases could exist.

**LLM Streaming/Partial Responses:**
- What's not tested: LLM returning partial JSON in tool calls, truncated responses, malformed tool definitions.
- Files: `llm/llm_test.go:256-263` (choice decoding)
- Risk: Malformed responses could crash the agent or cause confusing behavior.
- Priority: Medium - Relies on external API behavior.

**Memory Extraction Timeout:**
- What's not tested: Memory extraction hitting 60-second timeout, exceeding max iterations.
- Files: `agent/agent.go:964-1008`
- Risk: Extraction hangs or fails silently, user unaware memory wasn't saved.
- Priority: Medium - Background task, but important for memory retention.

**Hot-Load Agent Configuration Changes:**
- What's not tested: Adding/removing agents via web API and verifying hot-load behavior.
- Files: `agent/router.go:155-195` (tryHotLoad)
- Risk: Config changes could result in agents not loading or loading incorrectly.
- Priority: Medium - Dynamic configuration is powerful but untested.

---

*Concerns audit: 2026-02-26*
