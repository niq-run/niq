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
    let Some(window) = app.get_webview_window("main") else { return };
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
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(Shell::default())
        .invoke_handler(tauri::generate_handler![ui_ready, retry_gateway])
        .setup(|app| {
            install_menu(app)?;
            boot(app.handle().clone());
            Ok(())
        })
        .on_menu_event(|app, event| match event.id().0.as_str() {
            "reload" => {
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.eval("window.location.reload()");
                }
            }
            "open" => {
                let url = app.state::<Shell>().url.lock().unwrap().clone();
                if let Some(url) = url {
                    open_external(&url);
                }
            }
            "stop" => {
                if let Err(error) = gateway::stop_installed() {
                    gateway::log(&error);
                }
            }
            _ => {}
        })
        .run(tauri::generate_context!())
        .expect("error while running niq");
}
