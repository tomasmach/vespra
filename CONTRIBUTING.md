# Contributing to Mnemon-bot

## Philosophy

Keep the codebase as small as possible. No boilerplate, no speculative abstractions, no unnecessary indirection. When in doubt about patterns or implementation decisions, refer to [`../spacebot`](https://github.com/spacedriveapp/spacebot) as a reference.

---

## Before You Start

Read [GO_CODE_STYLE.md](./GO_CODE_STYLE.md) before writing any code. It covers naming conventions, error handling, logging, goroutine patterns, and SQL style. Consistency with existing code wins over personal preference.

---

## Development Setup

**Requirements:**

- Go 1.21+
- A C compiler (CGO is required for `go-sqlite3`)
- Discord bot token + OpenRouter API key for local testing

**Build and test:**

```bash
go build -o mnemon-bot .
go test ./...
```

---

## Code Style

Key rules (full reference in [GO_CODE_STYLE.md](./GO_CODE_STYLE.md)):

- Three import groups: stdlib → third-party → internal, each alphabetical
- Never abbreviate field names: `channelID` not `chID`, `serverID` not `sid`
- Wrap errors with `%w` and lowercase messages: `"query memories: %w"`
- Use `log/slog` with structured key-value pairs for all logging
- Pass `ctx context.Context` as the first parameter to any function that does I/O
- Prefer adding to existing files over creating new ones

---

## Testing

Write tests for critical user paths and high-risk logic. Prioritize integration tests at external boundaries (SQLite, HTTP). Focus on behavior and outputs, not implementation details.

**Test:** business logic, memory operations, config resolution, HTTP handlers.

**Skip:** trivial getters, TOML parsing, config file loading, experimental code.

```bash
go test ./...
```

---

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/). No scopes, no multi-line messages.

```
feat: add user authentication
fix: resolve memory leak in cache
refactor: simplify database query logic
docs: update API documentation
test: add unit tests for validation
chore: update dependencies
```

Valid prefixes: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `style`, `perf`.

---

## Branch Naming

Use prefixes matching commit types:

```
feat/add-web-search
fix/memory-leak
refactor/simplify-router
```

---

## Pull Requests

1. Fork the repo
2. Create a branch from `main`
3. Make your changes
4. Submit a PR

Keep scope small. One logical change per PR.
