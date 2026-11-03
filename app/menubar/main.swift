// niq menu bar — the `niq` report in a menu, with the project actions one
// level down.
//
// It owns no state: every refresh runs `niq status --json`, and every action is
// a `niq …` invocation, so the app can never disagree with the CLI or the
// control plane. The binary is looked up once at launch (NIQ_BIN, then the
// usual install locations, then a login shell's PATH — GUI apps inherit a
// minimal PATH).

import AppKit

// MARK: - Snapshot

private struct Snapshot: Decodable {
    let version: String
    let control: Control
    let projects: [Project]

    struct Control: Decodable {
        let running: Bool
        let pid: Int?
        let url: String?
        let log: String?
        let started: String?
    }

    struct Project: Decodable {
        let id: String
        let running: Bool
        let webui: Int
        let bus: Int
        let url: String?
        let log: String?
    }
}

// MARK: - App

final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let menu = NSMenu()
    private var snapshot: Snapshot?
    private var refreshing = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem.button?.image = Self.mark()
        statusItem.button?.toolTip = "niq"
        statusItem.menu = menu
        menu.delegate = self
        rebuild()
    }

    // The menu is rebuilt on open rather than polled, and built synchronously:
    // this runs just before the menu is drawn, so the first thing the user sees
    // is current. The command takes tens of milliseconds, and the alternative —
    // updating items while the menu is already on screen — moves them under the
    // cursor.
    func menuWillOpen(_ menu: NSMenu) {
        refreshing = false
        let result = run(["status", "--json"])
        if result.status == 0, let data = result.out.data(using: .utf8),
           let parsed = try? JSONDecoder().decode(Snapshot.self, from: data) {
            snapshot = parsed
            rebuild()
        } else {
            rebuild(error: result.err.isEmpty ? "niq status failed" : result.err)
        }
    }

    // MARK: Status item art

    /// The web UI's mark: three squares — one centred on top, two below — the
    /// 品 shape from the sidebar logo (pin.svg), drawn without its background
    /// tile so it works at menu bar size.
    ///
    /// A template image: macOS draws it black on a light menu bar and white on a
    /// dark one, which is the light/dark support — no per-appearance artwork to
    /// keep in sync.
    private static func mark() -> NSImage {
        let side: CGFloat = 18
        let cell: CGFloat = 5
        let gap: CGFloat = 1.5
        let image = NSImage(size: NSSize(width: side, height: side))
        image.lockFocus()
        NSColor.black.setFill()
        // AppKit's origin is bottom-left; the pattern is top-centre, bottom-left,
        // bottom-right.
        for (column, row) in [(1, 2), (0, 0), (2, 0)] {
            let origin = NSPoint(x: CGFloat(column) * (cell + gap), y: CGFloat(row) * (cell + gap))
            NSBezierPath(rect: NSRect(origin: origin, size: NSSize(width: cell, height: cell))).fill()
        }
        image.unlockFocus()
        image.isTemplate = true
        return image
    }

    // MARK: niq

    /// Runs `niq <args>`, returning stdout and stderr.
    @discardableResult
    private func run(_ args: [String], wait: Bool = true) -> (out: String, err: String, status: Int32) {
        guard let bin = Self.niqBinary() else {
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
        if wait {
            process.waitUntilExit()
        }
        let outData = out.fileHandleForReading.readDataToEndOfFile()
        let errData = err.fileHandleForReading.readDataToEndOfFile()
        return (String(decoding: outData, as: UTF8.self),
                String(decoding: errData, as: UTF8.self),
                process.terminationStatus)
    }

    private static var resolvedBinary: String?

    /// `--niq <path>`, handed over by niqm: the CLI next to the launcher is the
    /// one that matches this build, and LaunchServices does not pass the
    /// environment a terminal would have.
    private static let overrideBinary: String? = {
        let args = CommandLine.arguments
        for (index, arg) in args.enumerated() {
            if arg == "--niq", index + 1 < args.count { return args[index + 1] }
            if arg.hasPrefix("--niq=") { return String(arg.dropFirst("--niq=".count)) }
        }
        return nil
    }()

    private static func niqBinary() -> String? {
        if let cached = resolvedBinary, FileManager.default.isExecutableFile(atPath: cached) {
            return cached
        }
        if let override = overrideBinary, FileManager.default.isExecutableFile(atPath: override) {
            resolvedBinary = override
            return override
        }
        var candidates: [String] = []
        if let override = ProcessInfo.processInfo.environment["NIQ_BIN"], !override.isEmpty {
            candidates.append(override)
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        candidates += [
            "\(home)/.local/bin/niq",
            "/opt/homebrew/bin/niq",
            "/usr/local/bin/niq",
            "\(home)/.niq/bin/niq",
        ]
        for candidate in candidates where FileManager.default.isExecutableFile(atPath: candidate) {
            resolvedBinary = candidate
            return candidate
        }
        // Last resort: a login shell knows the PATH the user actually has.
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
                resolvedBinary = path
                return path
            }
        }
        return nil
    }

    // MARK: Refresh

    private func refresh() {
        guard !refreshing else { return }
        refreshing = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let result = self.run(["status", "--json"])
            var parsed: Snapshot?
            if result.status == 0, let data = result.out.data(using: .utf8) {
                parsed = try? JSONDecoder().decode(Snapshot.self, from: data)
            }
            DispatchQueue.main.async {
                if let parsed {
                    self.snapshot = parsed
                    self.rebuild()
                } else {
                    self.rebuild(error: result.err.isEmpty ? "niq status failed" : result.err)
                }
            }
        }
    }

    // MARK: Menu

    private func rebuild(error: String? = nil) {
        menu.removeAllItems()

        if let error {
            let item = NSMenuItem(title: error.trimmingCharacters(in: .whitespacesAndNewlines), action: nil, keyEquivalent: "")
            item.isEnabled = false
            menu.addItem(item)
            menu.addItem(.separator())
        } else if let snapshot {
            addControlItems(snapshot)
            menu.addItem(.separator())
            addProjectItems(snapshot)
            menu.addItem(.separator())
        }

        // No Refresh item: opening the menu already refreshes it (menuWillOpen).
        if let github = URL(string: "https://github.com/niq-run/niq") {
            menu.addItem(item("Open GitHub", #selector(openURL(_:)), represented: github))
        }

        let quitItem = NSMenuItem(title: "Quit", action: #selector(quitClicked), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
    }

    private func addControlItems(_ snapshot: Snapshot) {
        let control = snapshot.control
        if control.running, let urlString = control.url, let url = URL(string: urlString) {
            menu.addItem(item("Open WebUI", #selector(openURL(_:)), represented: url))
            menu.addItem(item("Stop", #selector(runCommand(_:)), represented: ["stop"]))
        } else {
            let openItem = item("Open WebUI", #selector(openURL(_:)), represented: nil, enabled: false)
            menu.addItem(openItem)
            menu.addItem(item("Start", #selector(runCommand(_:)), represented: ["start"]))
        }
    }

    private func addProjectItems(_ snapshot: Snapshot) {
        if snapshot.projects.isEmpty {
            let empty = NSMenuItem(title: "No Projects", action: nil, keyEquivalent: "")
            empty.isEnabled = false
            menu.addItem(empty)
            return
        }
        // One entry per project at the top level: the list is the menu, and a
        // "Projects" parent would only add a click.
        for project in snapshot.projects {
            let entry = NSMenuItem(title: "\(project.id) · \(project.running ? "running" : "stopped")", action: nil, keyEquivalent: "")
            let actions = NSMenu()

            // Only a running instance serves a page: a stopped one's URL is the
            // proxy's start prompt, and Start is already in this menu.
            if project.running, let urlString = project.url, let url = URL(string: urlString) {
                actions.addItem(item("Open WebUI", #selector(openURL(_:)), represented: url))
            } else {
                actions.addItem(item("Open WebUI", #selector(openURL(_:)), represented: nil, enabled: false))
            }
            actions.addItem(item("Start", #selector(runCommand(_:)), represented: ["project", "start", project.id], enabled: !project.running))
            actions.addItem(item("Stop", #selector(runCommand(_:)), represented: ["project", "stop", project.id], enabled: project.running))
            actions.addItem(item("Restart", #selector(runCommand(_:)), represented: ["project", "restart", project.id], enabled: project.running))

            entry.submenu = actions
            menu.addItem(entry)
        }
    }

    private func item(_ title: String, _ action: Selector, represented: Any? = nil, enabled: Bool = true) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
        item.target = self
        item.representedObject = represented
        item.isEnabled = enabled
        return item
    }

    // MARK: Actions

    @objc private func quitClicked() {
        NSApp.terminate(nil)
    }

    @objc private func openURL(_ sender: NSMenuItem) {
        guard let url = sender.representedObject as? URL else { return }
        NSWorkspace.shared.open(url)
    }

    @objc private func runCommand(_ sender: NSMenuItem) {
        guard let args = sender.representedObject as? [String] else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let result = self.run(args)
            DispatchQueue.main.async {
                if result.status != 0 {
                    self.report(args: args, stderr: result.err)
                }
                self.refresh()
            }
        }
    }

    private func report(args: [String], stderr: String) {
        let alert = NSAlert()
        alert.messageText = "niq \(args.joined(separator: " ")) failed"
        alert.informativeText = stderr.isEmpty ? "The command exited with an error." : stderr
        alert.alertStyle = .warning
        alert.runModal()
    }
}

// MARK: - main

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory) // menu bar only: no Dock icon, no windows
app.run()
