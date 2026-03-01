# Memory Improvements Design

## Problem

With 90+ memories on a server, the bot:
1. Can't find memories it already has — says "I don't remember" despite having the info stored
2. Saves duplicate/similar memories — cluttering the DB and degrading recall quality

## Solution Overview

Four changes: dedup-on-save, better recall filtering, FTS5 search, and smarter auto-recall.

---

## 1. Dedup-on-Save

**Where:** `memory/store.go` — `Save()` method

When `Save()` is called, before inserting:

1. Embed the new memory content (already happens)
2. Call `findSimilar(ctx, serverID, vector, 0.85)` — loads all embeddings for the server (reuses `allEmbeddings`), returns the single best match with cosine similarity >= 0.85
3. If a match is found:
   - If new content is longer (more detail) → update existing memory's content, re-embed, bump `updated_at`. Return `"Memory updated (id: <existing_id>)"`
   - If same length or shorter → skip. Return `"Memory already exists (id: <existing_id>)"`
4. If no match → insert as normal

Edge case: if embedding fails, skip dedup and save anyway (graceful degradation).

**Threshold:** 0.85 cosine similarity (catches near-identical rephrasings, safe for distinct topics).

---

## 2. Recall Improvements

**Where:** `memory/search.go`, `agent/agent.go`, `config/`

### A) Cosine similarity threshold: 0.35

After computing cosine similarity for all embeddings, filter out anything below 0.35 before passing to RRF. Only semantically relevant memories compete for top-N slots.

Threshold applies only to semantic search — keyword/FTS5 results pass through regardless (literal name matches should always be candidates).

### B) Configurable top_n, default 15

Add `memory_recall_limit` to config. Used in both auto-recall (agent.go) and as default for `memory_recall` tool. Default bumped from 10 to 15.

### C) Better system prompt formatting

Change from flat list to include importance and age:

```
## Relevant Memories
- [abc123] (importance: 0.8, 3 days ago) Tomas prefers dark roast coffee
- [def456] (importance: 0.6, 1 week ago) Tomas is working on a Discord bot project
```

---

## 3. FTS5 Full-Text Search

**Where:** `memory/store.go`, `memory/search.go`, new migration

### Schema

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content,
    content='memories',
    content_rowid='rowid'
);
```

### Sync

- On `Save()`: insert into `memories_fts`
- On `Forget()`: delete from `memories_fts`
- On dedup update (Section 1): delete old + insert new in `memories_fts`

### Search

Replace:
```sql
SELECT id FROM memories WHERE content LIKE ?
```
With:
```sql
SELECT m.id FROM memories m
JOIN memories_fts fts ON m.rowid = fts.rowid
WHERE m.server_id = ? AND m.forgotten = 0
AND memories_fts MATCH ?
ORDER BY rank
```

### Migration

New migration file that creates the FTS table and backfills from existing non-forgotten memories. Runs on startup.

---

## 4. Smarter Auto-Recall (Two-Pass)

**Where:** `agent/agent.go` — the auto-recall before each turn

Currently: one generic `Recall(ctx, msg.Content, serverID, 10)`.

Change to two-pass:

### Pass 1: User-specific memories

Query memories where `user_id` matches the message author's Discord ID. Fetch up to half of top_n. This surfaces facts about the person talking regardless of message content.

Example: user asks "what should I eat?" → their food preferences come up even though the message doesn't mention food preferences.

### Pass 2: Content-relevant memories

Existing semantic + FTS5 search across ALL server memories, using message text as query. Fetch up to top_n results.

### Merge

Combine both result sets, deduplicate by memory ID, user-specific memories get priority. Cap at configured top_n (default 15).

---

## Config Additions

```toml
[agent]
memory_recall_limit = 15          # auto-recall top_n (default 15)
memory_dedup_threshold = 0.85     # cosine similarity for dedup on save
memory_recall_threshold = 0.35    # minimum cosine similarity for recall
```

---

## Files to Modify

| File | Changes |
|------|---------|
| `memory/store.go` | `findSimilar()`, dedup logic in `Save()`, FTS sync in Save/Forget |
| `memory/search.go` | Similarity threshold, FTS5 query, `RecallForUser()` method |
| `memory/rrf.go` | No changes needed |
| `agent/agent.go` | Two-pass recall, configurable top_n, better memory formatting |
| `config/config.go` | New fields: `MemoryRecallLimit`, `MemoryDedupThreshold`, `MemoryRecallThreshold` |
| `tools/tools.go` | Update `memory_save` return messages, use config top_n as default |
| `migrations/0002_fts5.sql` | New migration for FTS5 table + backfill |

## Extraction Prompt

No changes — stays liberal. Dedup-on-save handles duplicates at storage level.
