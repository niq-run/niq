import Foundation

/// Resolves the running gateway's address without needing to find the `niq`
/// binary: the CLI records the bound address in `<niq root>/shell.json` when it
/// starts the gateway daemon (`{ "pid", "addr", "bound", "started" }`).
///
/// This is the fallback when `niq status --json` cannot be run (a GUI app may
/// not have `niq` on its PATH), so the client still knows where the gateway is.
enum GatewayDiscovery {
    /// The gateway URL from `<root>/shell.json`, or nil when the daemon is not
    /// recorded on this machine.
    static func shellStateURL() -> URL? {
        let env = ProcessInfo.processInfo.environment
        let root: URL
        if let home = env["NIQ_HOME"], !home.isEmpty {
            root = URL(fileURLWithPath: (home as NSString).expandingTildeInPath, isDirectory: true)
        } else {
            root = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".niq", isDirectory: true)
        }
        let file = root.appendingPathComponent("shell.json")
        guard let data = try? Data(contentsOf: file),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let bound = obj["bound"] as? String,
              let port = port(of: bound) else { return nil }
        return URL(string: "http://127.0.0.1:\(port)")
    }

    /// `"127.0.0.1:9527"`, `"[::]:9527"`, `":9527"` → `"9527"`.
    private static func port(of addr: String) -> String? {
        guard let idx = addr.lastIndex(of: ":") else { return nil }
        let p = String(addr[addr.index(after: idx)...])
        return p.isEmpty ? nil : p
    }
}
