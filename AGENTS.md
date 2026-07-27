# Agent Instructions

This repository intentionally exposes exactly four leaf commands: `version`,
`drafts create`, `drafts find`, and `posts get`.

Safety rules:

- Never add scheduling, publishing, sending, subscriber, Notes, analytics, or
  browser-login commands without a new reviewed product decision.
- Accept Substack session material only through
  `PP_SUBSTACK_SESSION_COOKIE`.
- Never print, persist, fixture, record, or include session material in an
  error.
- For `drafts find`, absence is authoritative only after a complete,
  uncapped scan of the draft, scheduled, and published feeds finds no exact
  marker. For `posts get`, absence is authoritative only after both the draft
  and global published-post endpoints return HTTP 404. Authentication,
  authorization, transport, rate-limit, parse, incomplete-feed, and ambiguity
  failures must remain errors.
- Write a failing test before changing behavior.
- Run `go test -race ./...`, `go vet ./...`, `govulncheck ./...`, and both
  required cross-builds before release.
