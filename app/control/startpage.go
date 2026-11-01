// The stand-in page for a project whose WebUI is not answering.
//
// /p/<id>/ is the address a user bookmarks, so it should never dead-end in a
// bare 502 just because the instance is stopped: the page keeps the address
// meaningful and puts the one action that matters — starting the project —
// in the middle of the screen.
package control

import (
	"fmt"
	"html"
	stdhttp "net/http"
	"strings"
)

// wantsHTML reports whether the request is a browser navigation (as opposed to
// the SPA's own fetch/EventSource calls, which want the plain error).
func wantsHTML(r *stdhttp.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// pageText is the two-language copy for the start page. The SPA has a full
// i18n layer; a server-rendered page only has Accept-Language, which covers
// the case this page exists for.
var pageText = map[string]map[string]string{
	"en": {
		"title":    "%s is not running",
		"note":     "The project's WebUI isn't answering. Start it to open this address.",
		"action":   "Start project",
		"starting": "Starting…",
		"control":  "control plane",
		"retry":    "retry",
		"failed":   "couldn't start it — check the project's log",
	},
	"zh": {
		"title":    "「%s」当前未运行",
		"note":     "项目 WebUI 没有响应。启动后即可打开这个地址。",
		"action":   "启动项目",
		"starting": "启动中…",
		"control":  "控制台",
		"retry":    "重试",
		"failed":   "启动失败，请查看项目日志",
	},
}

func pageLang(r *stdhttp.Request) string {
	if strings.Contains(strings.ToLower(r.Header.Get("Accept-Language")), "zh") {
		return "zh"
	}
	return "en"
}

// renderStartPage answers with the centered "not running — start it" card.
func renderStartPage(w stdhttp.ResponseWriter, r *stdhttp.Request, id string, port int) {
	lang := pageLang(r)
	text := pageText[lang]

	addr := "—"
	if port != 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", port)
	}
	page := strings.NewReplacer(
		"{{lang}}", lang,
		"{{title}}", html.EscapeString(fmt.Sprintf(text["title"], id)),
		"{{note}}", html.EscapeString(text["note"]),
		"{{addr}}", html.EscapeString(addr),
		"{{action}}", html.EscapeString(text["action"]),
		"{{starting}}", html.EscapeString(text["starting"]),
		"{{control}}", html.EscapeString(text["control"]),
		"{{retry}}", html.EscapeString(text["retry"]),
		"{{failed}}", html.EscapeString(text["failed"]),
		"{{id}}", html.EscapeString(id),
	).Replace(startPageTemplate)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(stdhttp.StatusBadGateway)
	fmt.Fprint(w, page)
}

const startPageTemplate = `<!doctype html>
<html lang="{{lang}}">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>niq · {{id}}</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; padding: 24px;
    display: flex; align-items: center; justify-content: center;
    font: 15px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
    background: #f5f5f7; color: #1c1c1e;
  }
  .card {
    width: 100%; max-width: 400px; padding: 36px 32px; text-align: center;
    background: #fff; border: 1px solid #e3e3e8; border-radius: 16px;
    box-shadow: 0 10px 40px rgba(0, 0, 0, .07);
  }
  .dot { font-size: 22px; line-height: 1; color: #c7c7cc; margin-bottom: 14px; }
  h1 { margin: 0 0 10px; font-size: 19px; font-weight: 600; letter-spacing: -.01em; }
  p { margin: 0 0 26px; color: #6c6c70; font-size: 14px; }
  code {
    font: 12px ui-monospace, SFMono-Regular, Menlo, monospace; color: #8e8e93;
    display: inline-block; margin-top: 8px;
  }
  button {
    width: 100%; padding: 13px 18px; font: inherit; font-weight: 600; color: #fff;
    background: #0a84ff; border: 0; border-radius: 11px; cursor: pointer;
    transition: background .15s ease;
  }
  button:hover:not(:disabled) { background: #0071e3; }
  button:disabled { opacity: .55; cursor: default; }
  .err { min-height: 18px; margin-top: 14px; font-size: 13px; color: #d70015; }
  .foot { margin-top: 20px; font-size: 12px; color: #8e8e93; }
  a { color: inherit; text-decoration: underline; text-underline-offset: 2px; }
  @media (prefers-color-scheme: dark) {
    body { background: #101012; color: #f2f2f7; }
    .card { background: #1b1b1e; border-color: #2c2c30; box-shadow: none; }
    .dot { color: #48484a; }
    p { color: #9b9ba1; }
    code { color: #8e8e93; }
  }
</style>
<div class="card">
  <div class="dot">●</div>
  <h1>{{title}}</h1>
  <p>{{note}}<br><code>{{addr}}</code></p>
  <button id="start" data-id="{{id}}" data-starting="{{starting}}" data-failed="{{failed}}">{{action}}</button>
  <div class="err" id="err"></div>
  <div class="foot"><a href="/">{{control}}</a> · <a href="" onclick="location.reload();return false">{{retry}}</a></div>
</div>
<script>
  const btn = document.getElementById('start')
  const err = document.getElementById('err')

  async function start() {
    const label = btn.textContent
    btn.disabled = true
    btn.textContent = btn.dataset.starting
    err.textContent = ''
    try {
      const res = await fetch('/api/projects/' + encodeURIComponent(btn.dataset.id) + '/start', { method: 'POST' })
      if (!res.ok) throw new Error((await res.text()).trim() || ('HTTP ' + res.status))
      // The handler answers 200 even when the project failed to come up; only a
      // reported port means the WebUI is actually there to load.
      const out = await res.json().catch(() => ({}))
      if (!out.webui_port) throw new Error(btn.dataset.failed)
      location.reload()
    } catch (e) {
      btn.disabled = false
      btn.textContent = label
      err.textContent = String((e && e.message) || e)
    }
  }
  btn.addEventListener('click', start)

  // Started from somewhere else (CLI, another tab)? Pick it up without a reload.
  setInterval(async () => {
    try {
      const list = await (await fetch('/api/projects')).json()
      const me = list.find(p => p.id === btn.dataset.id)
      if (me && me.running) location.reload()
    } catch (_) {}
  }, 3000)
</script>
</html>
`
