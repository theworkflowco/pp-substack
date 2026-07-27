---
name: pp-substack
description: Create and reconcile Substack newsletter drafts without scheduling, publishing, sending, or managing subscribers.
---

# pp-substack

Use this CLI when an agent must create a reviewed Substack draft, reconcile an
uncertain create by `gtme-issue:<uuid>` marker, or read strict
draft/scheduled/published state by external post ID.

Do not use it to edit, schedule, publish, send, manage subscribers, post Notes,
or query analytics. Those capabilities are deliberately absent.

## Authentication

Provide a complete authenticated Cookie header only through
`PP_SUBSTACK_SESSION_COOKIE`. Never put the cookie in arguments, logs, issue
text, or agent context.

## Recipes

```bash
pp-substack version --json
```

```bash
pp-substack drafts find \
  --publication gtmengineersearch \
  --correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d \
  --json
```

```bash
pp-substack drafts create \
  --publication gtmengineersearch \
  --title "GTM jobs this week" \
  --markdown-file ./issue.md \
  --correlation-marker gtme-issue:781260b8-b753-5d4f-a4a7-4df56a2cf77d \
  --json
```

```bash
pp-substack posts get \
  --publication gtmengineersearch \
  --post-id 208706412 \
  --json
```

Always run `drafts find` before retrying an uncertain create. A
`{"found":false}` result is authoritative only when the command exits `0`.

