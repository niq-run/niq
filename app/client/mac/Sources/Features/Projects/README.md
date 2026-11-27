# `Features/Projects` — the project catalog

The WebUI table shape: 项目 ID (with a 当前 marker) · 目录 · 端口 · 状态 · 操作.
Clicking a row selects it (reloads workers, reconnects the stream); 启动 / 停止 /
重启 call `/api/projects/{id}/{action}`.

- **新建** — a sheet with a project id + template (`POST /api/projects`), then it
  selects and connects the new project.
- **链接目录** — a server-side directory browser (`GET /api/projects/dirs?path=`)
  with 上一级 navigation; 链接此目录 posts `/api/projects/link {path}`.

Because a browser file input cannot expose an absolute path, the browsing
happens server-side — same as the WebUI.
