// A desktop shell around niq.
//
// The shell owns no UI of its own: it starts the bundled `niq` CLI (a Tauri
// sidecar), waits for the gateway, and then loads the gateway's own WebUI. That
// way there is exactly one build of the interface — the core repository's — and
// this app is only the installable wrapper that makes it double-clickable.

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod gateway;

use std::sync::Mutex;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::{AppHandle, Manager};
use tauri_plugin_shell::process::CommandEvent;
use tauri_plugin_shell::ShellExt;

/// Runs after the notification plugin's own shim, which is why the window is
/// built in code rather than in `tauri.conf.json`: a webview's own
/// initialization scripts are appended after the plugins', and only that order
/// lets this one wrap `window.Notification` instead of being overwritten.
const NOTIFICATION_CLICKS: &str = include_str!("notify.js");

/// Keeps the menu bar item alive: dropping a `TrayIcon` removes it.
struct Tray(#[allow(dead_code)] tauri::tray::TrayIcon);

/// Handshake state between the splash page and the gateway boot.
///
/// The window is created before the gateway answers, so it shows a local splash
/// (`ui/index.html`). The splash calls `ui_ready`; the boot thread fills in
/// either the URL or an error; `flush` performs the navigation when both sides
/// are ready, whichever finishes last.
#[derive(Default)]
struct Shell {
    url: Mutex<Option<String>>,
    error: Mutex<Option<String>>,
    ui_ready: Mutex<bool>,
}

fn flush(app: &AppHandle) {
    let state = app.state::<Shell>();
    if !*state.ui_ready.lock().unwrap() {
        return;
    }
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    // Take copies out of the guards first: keeping the guard alive inside the
    // `if let` would outlive `state` at the end of the function.
    let error = state.error.lock().unwrap().clone();
    if let Some(error) = error {
        // `window.niqFailed` is defined by the splash page.
        let _ = window.eval(&format!("window.niqFailed({error:?})"));
        return;
    }
    let url = state.url.lock().unwrap().clone();
    if let Some(url) = url {
        let _ = window.eval(&format!("window.location.replace({url:?})"));
    }
}

/// Spawn the bundled CLI as a sidecar (`bundle.externalBin` → `binaries/niq`).
fn spawn_sidecar(app: &AppHandle) -> Result<(), String> {
    let command = app
        .shell()
        .sidecar("niq")
        .map_err(|error| format!("sidecar 不可用：{error}"))?
        .args(["--local"]);
    let (mut rx, child) = command
        .spawn()
        .map_err(|error| format!("sidecar 启动失败：{error}"))?;

    // Drain the child's output so its pipes never block. The CLI daemonises and
    // exits; keeping both ends alive here lets us log a failure if it does not.
    tauri::async_runtime::spawn(async move {
        let _child = child;
        while let Some(event) = rx.recv().await {
            match event {
                CommandEvent::Stdout(line) | CommandEvent::Stderr(line) => {
                    let text = String::from_utf8_lossy(&line);
                    let text = text.trim_end();
                    if !text.is_empty() {
                        gateway::log(text);
                    }
                }
                CommandEvent::Terminated(payload) => {
                    gateway::log(&format!("cli exited with {:?}", payload.code));
                }
                _ => {}
            }
        }
    });
    Ok(())
}

fn boot(app: AppHandle) {
    {
        let state = app.state::<Shell>();
        *state.error.lock().unwrap() = None;
        *state.url.lock().unwrap() = None;
    }
    std::thread::spawn(move || {
        let sidecar_app = app.clone();
        let result = gateway::ensure_running(move || spawn_sidecar(&sidecar_app));
        {
            let state = app.state::<Shell>();
            match result {
                Ok(url) => *state.url.lock().unwrap() = Some(url),
                Err(error) => *state.error.lock().unwrap() = Some(error),
            }
        }
        flush(&app);
    });
}

// MARK: - Commands used by the splash page

#[tauri::command]
fn ui_ready(app: AppHandle) {
    *app.state::<Shell>().ui_ready.lock().unwrap() = true;
    flush(&app);
}

#[tauri::command]
fn retry_gateway(app: AppHandle) {
    boot(app);
}

// MARK: - Menu

fn install_menu(app: &tauri::App) -> tauri::Result<()> {
    let reload = MenuItem::with_id(app, "reload", "重新加载", true, Some("CmdOrCtrl+R"))?;
    let open = MenuItem::with_id(app, "open", "在浏览器中打开", true, None::<&str>)?;
    let stop = MenuItem::with_id(app, "stop", "停止网关", true, None::<&str>)?;
    let quit = PredefinedMenuItem::quit(app, Some("退出 niq"))?;

    let view = Submenu::with_items(app, "视图", true, &[&reload, &open])?;
    let gateway_menu = Submenu::with_items(app, "网关", true, &[&stop])?;
    let app_menu = Submenu::with_items(app, "niq", true, &[&quit])?;
    app.set_menu(Menu::with_items(app, &[&app_menu, &view, &gateway_menu])?)?;
    Ok(())
}

/// The menu bar item: the mark alone, without its tile — a template image, so
/// macOS tints it with the menu bar's own colour.
fn install_tray(app: &tauri::App) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "tray-show", "显示窗口", true, None::<&str>)?;
    let reload = MenuItem::with_id(app, "tray-reload", "重新加载", true, None::<&str>)?;
    let open = MenuItem::with_id(app, "tray-open", "在浏览器中打开", true, None::<&str>)?;
    let stop = MenuItem::with_id(app, "tray-stop", "停止网关", true, None::<&str>)?;
    let quit = PredefinedMenuItem::quit(app, Some("退出 niq"))?;
    let menu = Menu::with_items(app, &[&show, &reload, &open, &stop, &quit])?;

    let tray = tauri::tray::TrayIconBuilder::with_id("main")
        .icon(tauri::image::Image::from_bytes(include_bytes!(
            "../icons/tray.png"
        ))?)
        .icon_as_template(true)
        .tooltip("niq")
        .menu(&menu)
        .show_menu_on_left_click(true)
        .on_menu_event(|app, event| handle_menu(app, event.id().0.as_str()))
        .build(app)?;
    app.manage(Tray(tray));
    Ok(())
}

/// Menu actions live here once: the app menu and the menu bar item share them,
/// with a `tray-` prefix telling the two apart.
fn handle_menu(app: &AppHandle, id: &str) {
    match id {
        "show" | "tray-show" => show_main(app),
        "reload" | "tray-reload" => {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.eval("window.location.reload()");
            }
        }
        "open" | "tray-open" => {
            let url = app.state::<Shell>().url.lock().unwrap().clone();
            if let Some(url) = url {
                open_external(&url);
            }
        }
        "stop" | "tray-stop" => {
            if let Err(error) = gateway::stop_installed() {
                gateway::log(&error);
            }
        }
        _ => {}
    }
}

/// Bring the window back. After a close it is hidden rather than destroyed, so
/// this reuses the same webview — and the WebUI keeps its state.
fn show_main(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.set_focus();
    }
}

#[cfg(target_os = "macos")]
fn open_external(url: &str) {
    let _ = std::process::Command::new("open").arg(url).spawn();
}

#[cfg(target_os = "windows")]
fn open_external(url: &str) {
    let _ = std::process::Command::new("rundll32")
        .args(["url.dll,FileProtocolHandler", url])
        .spawn();
}

#[cfg(all(unix, not(target_os = "macos")))]
fn open_external(url: &str) {
    let _ = std::process::Command::new("xdg-open").arg(url).spawn();
}

fn main() {
    let app = tauri::Builder::default()
        // Replaces `window.Notification` with a system-backed shim at document
        // start (see the plugin's init script), which is how the WebUI's
        // browser-notification code path works unchanged inside the shell.
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_shell::init())
        .manage(Shell::default())
        .invoke_handler(tauri::generate_handler![ui_ready, retry_gateway])
        .setup(|app| {
            install_menu(app)?;
            // The window is built here, not in tauri.conf.json, so it can carry
            // the notification-click script (see NOTIFICATION_CLICKS).
            tauri::WebviewWindowBuilder::new(
                app,
                "main",
                tauri::WebviewUrl::App("index.html".into()),
            )
            .title("")
            .inner_size(1280.0, 820.0)
            .min_inner_size(900.0, 600.0)
            .center()
            .initialization_script(NOTIFICATION_CLICKS)
            .build()?;
            install_tray(app)?;
            boot(app.handle().clone());
            Ok(())
        })
        .on_menu_event(|app, event| handle_menu(app, event.id().0.as_str()))
        // Closing the window hides it instead of destroying it. This is a gateway
        // client that lives in the menu bar: putting the window away should leave
        // the shell — and the WebUI's state — intact. Quitting stays explicit:
        // ⌘Q, or 退出 in the menu bar item.
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("error while running niq");

    app.run(|_app, _event| {
        // Clicking the Dock icon (or otherwise re-opening the app) with nothing
        // on screen brings the hidden window back. macOS only.
        #[cfg(target_os = "macos")]
        if let tauri::RunEvent::Reopen {
            has_visible_windows,
            ..
        } = _event
        {
            if !has_visible_windows {
                show_main(_app);
            }
        }
    });
}
