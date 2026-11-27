// Makes a click on a system notification come back to the page.
//
// tauri-plugin-notification replaces `window.Notification` with a shim that
// posts real notifications but drops clicks: the WebUI's `n.onclick` would
// simply never run. This script runs after that shim (a webview's own
// initialization scripts are appended after the plugins'), wraps it, and
// keeps the handler alive by handing the plugin an `id` it echoes back in the
// `tap` action.
//
// Nothing here is niq-specific: any page that uses `new Notification(...)` and
// `notification.onclick` gets the behaviour the browser would have given it.
(() => {
  const tauri = window.__TAURI__;
  if (!tauri || !tauri.notification) return;

  const Raw = window.Notification;
  if (!Raw) return;

  /** id -> the object the page got back, so its onclick can be read on tap */
  const pending = new Map();
  let sequence = 1000;

  // The page identifies a notification by `tag` (stable per request); the
  // plugin identifies it by an integer `id`. Hash the tag so a re-posted
  // notification replaces the earlier record instead of piling up.
  function idForTag(tag) {
    let hash = 0;
    for (let i = 0; i < tag.length; i += 1) hash = (hash * 31 + tag.charCodeAt(i)) | 0;
    return Math.abs(hash) || 1;
  }

  function Notification(title, options) {
    const settings = Object.assign({}, options || {});
    const tag = settings.tag == null ? '' : String(settings.tag);
    const id = tag ? idForTag(tag) : (sequence += 1);
    delete settings.tag;
    settings.id = id;

    const handle = {
      id,
      onclick: null,
      close() {
        pending.delete(id);
      },
    };
    pending.set(id, handle);

    try {
      new Raw(title, settings);
    } catch (error) {
      // The plugin refuses when the OS has notifications turned off for us;
      // the page still gets its handle so its own bookkeeping stays intact.
      pending.delete(id);
      throw error;
    }
    return handle;
  }

  Notification.requestPermission = Raw.requestPermission.bind(Raw);
  Object.defineProperty(Notification, 'permission', {
    get: () => Raw.permission,
    set: (value) => {
      Raw.permission = value;
    },
  });

  tauri.notification.onAction((event) => {
    if (!event || event.actionId !== 'tap') return;
    const id = event.notification && event.notification.id;
    const handle = pending.get(id);
    if (!handle || typeof handle.onclick !== 'function') return;
    pending.delete(id);
    try {
      handle.onclick();
    } catch (error) {
      console.error(error);
    }
  });

  window.Notification = Notification;
})();
