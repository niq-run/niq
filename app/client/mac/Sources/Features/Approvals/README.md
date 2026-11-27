# `Features/Approvals` — the approver's queue

`ApprovalsView` reads `GET /p/{id}/api/approvals` (pending first, then the
decided history) and renders one card per entry:

- status dot + title (`action` / `tool` / `path`), 待处理 / 已批准 / 已拒绝, time;
- source worker, tool and path metadata;
- the request payload as collapsible JSON;
- for pending entries, **批准** / **拒绝**
  (`POST /api/approvals/{id}/decision {approved, note}`) and a 调用链 link.

The talk view's header shows a `有 N 条审批待处理` pill while anything is pending;
clicking it opens this view.

Refresh is manual (the view loads on appear, and after every decision). A push
channel (an SSE watcher on `approval.request`) would remove the refresh need.

Note: `approval.notes` are accepted by the API but the composer for a note is
not wired yet — decisions are sent with an empty note.
