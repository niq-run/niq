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

    // The menu is rebuilt on open rather than polled: opening it is the moment
    // the answer has to be true, and the command takes a few milliseconds.
    func menuWillOpen(_ menu: NSMenu) {
        refresh()
    }

    // MARK: Status item art

    /// The wordmark as a template image — the menu bar tints it for light and
    /// dark, and no asset has to ship with the app.
    private static func mark() -> NSImage {
        let size = NSSize(width: 26, height: 18)
        let image = NSImage(size: size)
        image.lockFocus()
        let text = "niq" as NSString
        let attrs: [NSAttributedString.Key: Any] = [
            .font: NSFont.monospacedSystemFont(ofSize: 11.5, weight: .bold),
            .foregroundColor: NSColor.black,
        ]
        let bounds = text.size(withAttributes: attrs)
        text.draw(at: NSPoint(x: (size.width - bounds.width) / 2, y: (size.height - bounds.height) / 2),
                  withAttributes: attrs)
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

    private static func niqBinary() -> String? {
        if let cached = resolvedBinary, FileManager.default.isExecutableFile(atPath: cached) {
            return cached
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

        let versionItem = NSMenuItem(title: snapshot.map { "niq \($0.version)" } ?? "niq", action: nil, keyEquivalent: "")
        versionItem.isEnabled = false
        menu.addItem(versionItem)
        menu.addItem(.separator())

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

        let refreshItem = NSMenuItem(title: "Refresh", action: #selector(refreshClicked), keyEquivalent: "r")
        refreshItem.target = self
        menu.addItem(refreshItem)

        let quitItem = NSMenuItem(title: "Quit", action: #selector(quitClicked), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
    }

    private func addControlItems(_ snapshot: Snapshot) {
        let control = snapshot.control
        let state = control.running ? "control running · pid \(control.pid ?? 0)" : "control stopped"
        let stateItem = NSMenuItem(title: state, action: nil, keyEquivalent: "")
        stateItem.isEnabled = false
        menu.addItem(stateItem)

        if control.running, let urlString = control.url, let url = URL(string: urlString) {
            menu.addItem(item("WebUI   \(urlString)", #selector(openURL(_:)), represented: url))
        } else {
            let item = NSMenuItem(title: "WebUI   —", action: nil, keyEquivalent: "")
            item.isEnabled = false
            menu.addItem(item)
        }

        if let log = control.log {
            menu.addItem(item("Log   \(short(log))", #selector(openFile(_:)), represented: URL(fileURLWithPath: log)))
        }

        if control.running {
            menu.addItem(item("Restart niq", #selector(runCommand(_:)), represented: ["restart"]))
            menu.addItem(item("Restart niq and projects", #selector(runCommand(_:)), represented: ["restart", "-p"]))
            menu.addItem(item("Stop niq", #selector(runCommand(_:)), represented: ["stop"]))
        } else {
            menu.addItem(item("Start niq", #selector(runCommand(_:)), represented: ["start"]))
        }
    }

    private func addProjectItems(_ snapshot: Snapshot) {
        let root = NSMenuItem(title: "Projects", action: nil, keyEquivalent: "")
        let submenu = NSMenu()
        if snapshot.projects.isEmpty {
            let empty = NSMenuItem(title: "(none)", action: nil, keyEquivalent: "")
            empty.isEnabled = false
            submenu.addItem(empty)
        }
        for project in snapshot.projects {
            let entry = NSMenuItem(title: "[\(project.id)]  \(project.running ? "running" : "stopped")", action: nil, keyEquivalent: "")
            let actions = NSMenu()

            if let urlString = project.url, let url = URL(string: urlString) {
                actions.addItem(item("Open WebUI", #selector(openURL(_:)), represented: url))
            }
            let ports = NSMenuItem(title: "webui \(project.webui)   bus \(project.bus)", action: nil, keyEquivalent: "")
            ports.isEnabled = false
            actions.addItem(ports)
            actions.addItem(.separator())

            // The same three entries the CLI's action menu always lists; the ones
            // an instance cannot perform are disabled rather than absent.
            actions.addItem(item("Start", #selector(runCommand(_:)), represented: ["project", "start", project.id], enabled: !project.running))
            actions.addItem(item("Stop", #selector(runCommand(_:)), represented: ["project", "stop", project.id], enabled: project.running))
            actions.addItem(item("Restart", #selector(runCommand(_:)), represented: ["project", "restart", project.id], enabled: project.running))

            entry.submenu = actions
            submenu.addItem(entry)
        }
        root.submenu = submenu
        menu.addItem(root)
    }

    private func item(_ title: String, _ action: Selector, represented: Any? = nil, enabled: Bool = true) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
        item.target = self
        item.representedObject = represented
        item.isEnabled = enabled
        return item
    }

    private func short(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return path.hasPrefix(home) ? "~" + path.dropFirst(home.count) : path
    }

    // MARK: Actions

    @objc private func refreshClicked() {
        refresh()
    }

    @objc private func quitClicked() {
        NSApp.terminate(nil)
    }

    @objc private func openURL(_ sender: NSMenuItem) {
        guard let url = sender.representedObject as? URL else { return }
        NSWorkspace.shared.open(url)
    }

    @objc private func openFile(_ sender: NSMenuItem) {
        guard let url = sender.representedObject as? URL else { return }
        // Reveal, so the log opens in whatever the user edits text with.
        NSWorkspace.shared.activateFileViewerSelecting([url])
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
