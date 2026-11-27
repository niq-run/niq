//! Finding, starting and probing the niq gateway.
//!
//! A client never implements the protocol: it launches the `niq` CLI (which owns
//! the control plane) and then talks to the gateway over HTTP. The bundled
//! `niq` is spawned as a Tauri **sidecar** (see `main.rs`); this module only
//! needs to answer "where is the gateway, and is it up?" — with nothing but the
//! standard library.

use std::io::{Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, Instant};

/// The gateway's remembered default address (`~/.niq/common/config.json`).
pub const DEFAULT_URL: &str = "http://127.0.0.1:9527";

pub fn log(message: &str) {
    eprintln!("[niq-desktop] {message}");
}

// MARK: - The CLI on this machine

fn is_executable(path: &Path) -> bool {
    let Ok(meta) = std::fs::metadata(path) else { return false };
    if !meta.is_file() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        meta.permissions().mode() & 0o111 != 0
    }
    #[cfg(not(unix))]
    {
        true
    }
}

/// A `niq` CLI installed on this machine (not the bundled sidecar):
/// `NIQ_BIN`, then `PATH`, then the usual install prefixes.
///
/// The sidecar is the normal path; this exists so a developer build works
/// against a CLI that is already installed, and so `niq stop` can be issued
/// when the sidecar never started.
pub fn installed_binary() -> Option<PathBuf> {
    if let Ok(value) = std::env::var("NIQ_BIN") {
        let path = PathBuf::from(value);
        if is_executable(&path) {
            return Some(path);
        }
        log(&format!("NIQ_BIN={} is not executable", path.display()));
    }

    if let Some(paths) = std::env::var_os("PATH") {
        for dir in std::env::split_paths(&paths) {
            for name in ["niq", "niq.exe"] {
                let candidate = dir.join(name);
                if is_executable(&candidate) {
                    return Some(candidate);
                }
            }
        }
    }

    for fallback in ["/opt/homebrew/bin/niq", "/usr/local/bin/niq", "/usr/bin/niq"] {
        let candidate = PathBuf::from(fallback);
        if is_executable(&candidate) {
            return Some(candidate);
        }
    }
    None
}

// MARK: - Probing

fn parse_authority(url: &str) -> Option<(String, u16)> {
    let rest = url.strip_prefix("http://")?;
    let authority = rest.split('/').next()?;
    match authority.rsplit_once(':') {
        Some((host, port)) => Some((host.to_string(), port.parse().ok()?)),
        None => Some((authority.to_string(), 80)),
    }
}

/// A minimal HTTP GET: is `path` answering 200 at `url`? Reads only the status
/// line, so it is cheap enough to poll twice a second while the gateway boots.
pub fn request_ok(url: &str, path: &str) -> bool {
    let Some((host, port)) = parse_authority(url) else { return false };
    let Ok(mut addrs) = (host.as_str(), port).to_socket_addrs() else { return false };
    let Some(addr) = addrs.next() else { return false };
    let Ok(mut stream) = TcpStream::connect_timeout(&addr, Duration::from_millis(500)) else {
        return false;
    };
    let _ = stream.set_read_timeout(Some(Duration::from_millis(800)));
    let request = format!("GET {path} HTTP/1.1\r\nHost: {host}:{port}\r\nConnection: close\r\n\r\n");
    if stream.write_all(request.as_bytes()).is_err() {
        return false;
    }
    let mut buffer = [0u8; 32];
    match stream.read(&mut buffer) {
        Ok(n) if n > 0 => {
            let head = String::from_utf8_lossy(&buffer[..n]);
            head.starts_with("HTTP/1.1 200") || head.starts_with("HTTP/1.0 200")
        }
        _ => false,
    }
}

/// The address the CLI recorded in `<niq root>/shell.json`
/// (`{"pid","addr","bound","started"}`), without needing a binary to run.
pub fn shell_record_url() -> Option<String> {
    let root = match std::env::var("NIQ_HOME") {
        Ok(home) if !home.trim().is_empty() => PathBuf::from(home),
        _ => PathBuf::from(std::env::var("HOME").ok()?).join(".niq"),
    };
    let text = std::fs::read_to_string(root.join("shell.json")).ok()?;
    let value: serde_json::Value = serde_json::from_str(&text).ok()?;
    let bound = value.get("bound")?.as_str()?;
    // "127.0.0.1:9527" | "[::]:9527" | ":9527" → the port (loopback only).
    let port = bound.rsplit_once(':')?.1;
    if port.is_empty() {
        return None;
    }
    Some(format!("http://127.0.0.1:{port}"))
}

/// `niq status --json` → `gateway.url`.
pub fn status_url(bin: &Path) -> Option<String> {
    let output = Command::new(bin).args(["status", "--json"]).output().ok()?;
    if !output.status.success() {
        return None;
    }
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).ok()?;
    let url = value.get("gateway")?.get("url")?.as_str()?;
    Some(url.trim_end_matches('/').to_string())
}

/// Any live gateway we can find, cheapest check first.
fn live_url() -> Option<String> {
    if let Some(url) = shell_record_url() {
        if request_ok(&url, "/api/projects") {
            return Some(url);
        }
    }
    if let Some(bin) = installed_binary() {
        if let Some(url) = status_url(&bin) {
            if request_ok(&url, "/api/projects") {
                return Some(url);
            }
        }
    }
    if request_ok(DEFAULT_URL, "/api/projects") {
        return Some(DEFAULT_URL.to_string());
    }
    None
}

// MARK: - Ensure running

/// Resolve a live gateway, starting the control plane if nothing answers.
///
/// `spawn_cli` starts the CLI — in the app it spawns the bundled sidecar; if it
/// fails (no sidecar in a dev build) we fall back to an installed `niq`.
///
/// Returns the base URL to load (`http://host:port`, no trailing slash).
pub fn ensure_running(spawn_cli: impl FnOnce() -> Result<(), String>) -> Result<String, String> {
    // 1. An explicit override always wins (and never starts a process).
    if let Ok(explicit) = std::env::var("NIQ_GATEWAY_URL") {
        let url = explicit.trim_end_matches('/').to_string();
        if request_ok(&url, "/api/projects") {
            log(&format!("using NIQ_GATEWAY_URL={url}"));
            return Ok(url);
        }
        return Err(format!("NIQ_GATEWAY_URL={url} 没有响应"));
    }

    // 2. Someone is already serving (another client, `niq w`, or a terminal).
    if let Some(url) = live_url() {
        log(&format!("gateway already running at {url}"));
        return Ok(url);
    }

    // 3. Start it. The sidecar is the product; a PATH binary keeps dev builds
    //    working. `--local` pins the listen address to loopback for this run.
    log("starting the control plane…");
    if let Err(error) = spawn_cli() {
        log(&format!("sidecar unavailable ({error}); trying an installed niq"));
        let bin = installed_binary().ok_or_else(|| {
            "找不到 niq：安装包里的 sidecar 未能启动，本机也没有安装 niq。".to_string()
        })?;
        Command::new(&bin)
            .arg("--local")
            .spawn()
            .map_err(|error| format!("启动 {} 失败：{error}", bin.display()))?;
    }

    let deadline = Instant::now() + Duration::from_secs(40);
    loop {
        if let Some(url) = live_url() {
            log(&format!("gateway ready at {url}"));
            return Ok(url);
        }
        if Instant::now() > deadline {
            break;
        }
        std::thread::sleep(Duration::from_millis(300));
    }
    Err("niq 网关在 40 秒内没有就绪。请检查 `niq status`，或查看 ~/.niq 下的日志。".to_string())
}

/// `niq stop` for an installed CLI (used when no sidecar handle exists).
pub fn stop_installed() -> Result<(), String> {
    let bin = installed_binary().ok_or_else(|| "找不到 niq CLI".to_string())?;
    let status = Command::new(bin)
        .arg("stop")
        .status()
        .map_err(|error| format!("执行 niq stop 失败：{error}"))?;
    if status.success() {
        Ok(())
    } else {
        Err(format!("niq stop 退出码 {}", status.code().unwrap_or(-1)))
    }
}
