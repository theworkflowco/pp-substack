# Agent Instructions

The approved command contract contains exactly seven leaf command shapes:
`version`, `drafts create`, `drafts compare`, `drafts find`, `drafts update`,
`posts get`, and `images upload`. All seven are exposed. `drafts update` is limited to changing
the title and body of unscheduled, unpublished drafts. `images upload` sends
one PNG or JPEG to the publication's image endpoint and prints the durable
URL; it never touches drafts or posts.

Safety rules:

- `drafts update` may change only the title, subtitle, and body of an existing draft. It
  must read lifecycle state immediately before mutation and refuse scheduled
  or published posts.
- No scheduling, publishing, sending, subscriber, Notes, analytics, or
  browser-login commands are approved.
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
