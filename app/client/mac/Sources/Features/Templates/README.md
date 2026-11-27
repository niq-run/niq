# `Features/Templates` — project templates

The control-plane template catalog (`/api/templates`).

- **List:** template names; right-click (or the detail 删除) removes one.
- **Editor:** the template's JSON, editable and saved with
  `PUT /api/templates/{name}`; the parse error is shown inline before saving.
- **新建:** a sheet with a name and an optional 复制自 source (blank document when
  none) → `POST /api/templates {id, template}`.
- **导入:** an `NSOpenPanel` for a `.zip` package → multipart
  `POST /api/templates/import`.

Templates are control-plane resources, so this view works without a selected
project (unlike the project-scoped surfaces).
