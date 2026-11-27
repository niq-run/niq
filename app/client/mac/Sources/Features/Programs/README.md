# `Features/Programs` — the project's programs

Programs are the project's reusable instruction documents (playbooks and
instructions) served by the program worker.

- **List:** name · 表单 (提示 / 脚本) · 内容类型 (指令 / 手册) · description ·
  sub-content count, with search.
- **Detail:** metadata, sub-content paths, and the entry body rendered as
  Markdown.
- **Create / edit:** a sheet with name (new only), content type, description,
  tags and the Markdown body; saves with `PUT /api/programs/{name}` (the
  program worker upserts).
- **Delete:** `DELETE /api/programs/{name}`, behind an `NSAlert` confirmation.

The vocabulary is the WebUI's, not MIME types: `content_type` is
`instruction` / `playbook`, `form_type` is `prompt` / `script`
(`contentLabel` / `formLabel` at the bottom of the file).
