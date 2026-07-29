# pp-substack

`pp-substack` creates and safely updates Substack newsletter drafts and reads
their lifecycle state for reconciliation. The CLI never schedules, publishes,
sends, or manages subscribers.

Substack does not publish a supported API for these writer workflows. This
client follows browser-observed endpoints and therefore fails loudly when the
wire contract changes.

## Quick Start

Read a complete authenticated Cookie header into the process environment
without placing it in shell history:

```bash
read -rs PP_SUBSTACK_SESSION_COOKIE
export PP_SUBSTACK_SESSION_COOKIE
```

Paste the value at the silent prompt and press Enter. For automation, inject
the variable from the runtime's encrypted secret store.

Reconcile the marker before creating:

```bash
pp-substack drafts find \
  --publication gtmengineersearch \
  --correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d \
  --json
```

Create only when that command exits `0` with `{"found":false}`:

```bash
pp-substack drafts create \
  --publication gtmengineersearch \
  --title "GTM jobs this week" \
  --markdown-file ./issue.md \
  --correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d \
  --json
```

The cookie is accepted only from `PP_SUBSTACK_SESSION_COOKIE`. It is never
accepted as an argument, written to local state, or included in errors.

To update a draft, first commit the rendered Markdown so the intended content
is durable and reviewable. Then read the post's current lifecycle state:

```bash
pp-substack posts get \
  --publication gtmengineersearch \
  --post-id 208706412 \
  --json
```

Run the mutation only when that command exits `0` with `status` equal to
`draft`:

```bash
pp-substack drafts update \
  --publication gtmengineersearch \
  --post-id 208706412 \
  --title "Updated GTM jobs this week" \
  --markdown-file ./issue.md \
  --correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d \
  --json
```

After an ambiguous update error, reconcile with `posts get`. Never retry the
mutation merely because its response was lost.

## Command Contract

The approved command contract exposes exactly five shapes:

```text
pp-substack version --json
pp-substack drafts create --publication <slug> --title <title> \
  --markdown-file <path> --correlation-marker <marker> --json
pp-substack drafts find --publication <slug> \
  --correlation-marker <marker> --json
pp-substack drafts update --publication <slug> --post-id <id> \
  --title <title> --markdown-file <path> \
  --correlation-marker <marker> --json
pp-substack posts get --publication <slug> --post-id <id> --json
```

`drafts update` changes only the title and body of an existing draft. It reads
lifecycle state immediately before mutation and refuses scheduled or published
posts. No scheduling, publishing, sending, subscriber, Notes, analytics, or
browser-login commands are approved.

Every automation command requires `--json`. `drafts find` and `posts get`
return `{"found":false}` when absence is authoritative; the `post` key is
omitted. Authentication, authorization, transport, rate-limit, ambiguity, and
response-contract failures remain errors.

Status values are strict:

- `draft`: both lifecycle timestamps are `null`.
- `scheduled`: `scheduled_at` is RFC 3339 and `published_at` is `null`.
- `published`: `published_at` is RFC 3339; `scheduled_at` is RFC 3339 or
  `null`.

For `draft` and `scheduled`, `post_url` is the writer-management URL. For
`published`, `post_url` is the canonical public reader URL returned by the
published endpoint. It must be an HTTPS `/p/<slug>` URL on the requested
publication host. A missing, unsafe, management, or cross-publication canonical
URL is a contract error.

The correlation marker must be a visible `gtme-issue:<uuid>` token that occurs
exactly once in the Markdown. The converter supports heading levels 1–6,
paragraphs, bullet lists, strong and italic emphasis, horizontal rules,
absolute HTTP(S) links, HTML entities, and Markdown escapes used by the GTME
newsletter composer.

## Agent Usage

Use `drafts find` before every create attempt. Treat a non-zero exit as an
unknown external state unless the error itself establishes otherwise. Never
retry `drafts create` merely because the caller did not receive its response;
reconcile by marker first.

Before an update, commit the Markdown, call `posts get`, and proceed only when
the post status is `draft`. After any ambiguous update result, call `posts get`
to reconcile. Never automatically retry `drafts update` merely because its
response was lost.

Substack's observed create request has no externally enforced idempotency
header or request field. The correlation marker supports reconciliation but
does not make the mutation itself idempotent.

Exit codes:

- `0`: success, including authoritative not-found
- `2`: invalid command or flags
- `3`: missing/expired authentication or permission denied
- `4`: unnormalized raw resource absence
- `5`: rate limit or remote service failure
- `7`: response or local contract failure

## Health Check

Confirm the installed binary:

```bash
pp-substack version --json
```

Confirm authenticated read access without creating anything:

```bash
pp-substack drafts find \
  --publication gtmengineersearch \
  --correlation-marker gtme-issue:00000000-0000-0000-0000-000000000000 \
  --json
```

A healthy empty result is `{"found":false}`.

## Troubleshooting

- `PP_SUBSTACK_SESSION_COOKIE is required`: inject the complete Cookie header
  into the child process environment.
- HTTP `401` or `403`: the browser session expired or lacks writer access.
  Re-authenticate manually and replace the encrypted runtime secret.
- A capped-feed or multiple-post error: stop automatic creation and reconcile
  in the Substack UI. The CLI refuses a result that could create duplicates.
- A JSON decode or marker error: Substack changed its private response shape,
  or the marker did not survive. Use the rendered Markdown paste fallback
  until the client is reviewed.

## Cookbook

Find a draft, scheduled post, or published post by marker:

```bash
pp-substack drafts find \
  --publication gtmengineersearch \
  --correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d \
  --json
```

Read lifecycle state by the stored external ID:

```bash
pp-substack posts get \
  --publication gtmengineersearch \
  --post-id 208706412 \
  --json
```

## Release

Tags produce only the two required release archives:

- `pp-substack_darwin_arm64.tar.gz`
- `pp-substack_linux_amd64.tar.gz`

`checksums.txt` contains SHA-256 digests. Builds use `CGO_ENABLED=0`,
`-trimpath`, and an empty Go build ID.
