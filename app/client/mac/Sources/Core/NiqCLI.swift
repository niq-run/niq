import Foundation

/// Locates and drives the `niq` CLI — the entry point for a third-party client
/// (see doc/how-to-build-a-client.md §1 "Who launches whom").
enum NiqCLI {
    private static var resolved: String?

    static func binary() -> String? {
        if let resolved, FileManager.default.isExecutableFile(atPath: resolved) { return resolved }
        if let override = ProcessInfo.processInfo.environment["NIQ_BIN"], !override.isEmpty,
           FileManager.default.isExecutableFile(atPath: override) {
            resolved = override
            return override
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let candidates = [
            "\(home)/.local/bin/niq",
            "/opt/homebrew/bin/niq",
            "/usr/local/bin/niq",
            "\(home)/.niq/bin/niq",
        ]
        for candidate in candidates where FileManager.default.isExecutableFile(atPath: candidate) {
            resolved = candidate
            return candidate
        }
        // Last resort: a login shell knows the PATH the user actually has
        // (a GUI app inherits a minimal one).
        let shell = Process()
        shell.executableURL = URL(fileURLWithPath: "/bin/zsh")
        shell.arguments = ["-lc", "command -v niq"]
        let pipe = Pipe()
        shell.standardOutput = pipe
        shell.standardError = FileHandle.nullDevice
        if (try? shell.run()) != nil {
            shell.waitUntilExit()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let path = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            if !path.isEmpty, FileManager.default.isExecutableFile(atPath: path) {
                resolved = path
                return path
            }
        }
        return nil
    }

    @discardableResult
    static func run(_ args: [String]) -> (out: String, err: String, status: Int32) {
        guard let bin = binary() else {
            return ("", "niq was not found (looked at NIQ_BIN, ~/.local/bin, Homebrew, ~/.niq/bin, PATH)", 127)
        }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: bin)
        process.arguments = args
        let out = Pipe(), err = Pipe()
        process.standardOutput = out
        process.standardError = err
        do {
            try process.run()
        } catch {
            return ("", "could not run \(bin): \(error.localizedDescription)", 1)
        }
        process.waitUntilExit()
        let outData = out.fileHandleForReading.readDataToEndOfFile()
        let errData = err.fileHandleForReading.readDataToEndOfFile()
        return (String(decoding: outData, as: UTF8.self),
                String(decoding: errData, as: UTF8.self),
                process.terminationStatus)
    }

    static func status() -> StatusSnapshot? {
        let result = run(["status", "--json"])
        guard result.status == 0, let data = result.out.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(StatusSnapshot.self, from: data)
    }

    /// Bare `niq` is a control-plane start: it brings the gateway up in the
    /// background and returns.
    static func startGateway() { run([]) }
    static func stopGateway() { run(["stop"]) }
}
