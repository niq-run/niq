import SwiftUI
import AppKit

@main
struct NiqApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = AppModel()

    var body: some Scene {
        WindowGroup("niq") {
            ContentView()
                .environmentObject(model)
                .task { await model.boot() }
        }
        .defaultSize(width: 1280, height: 840)
        .commands {
            CommandGroup(after: .sidebar) {
                Divider()
                Button("对话") { model.section = .talk }
                    .keyboardShortcut("1", modifiers: .command)
                Button("事件") { model.section = .events }
                    .keyboardShortcut("2", modifiers: .command)
                Button("审批") { model.section = .approvals }
                    .keyboardShortcut("3", modifiers: .command)
                Button("工作单元") { model.section = .workers }
                    .keyboardShortcut("4", modifiers: .command)
                Button("MCP 工作单元") { model.section = .mcp }
                    .keyboardShortcut("5", modifiers: .command)
                Button("程序") { model.section = .programs }
                    .keyboardShortcut("6", modifiers: .command)
                Button("项目") { model.section = .projects }
                    .keyboardShortcut("7", modifiers: .command)
                Button("模板") { model.section = .templates }
                    .keyboardShortcut("8", modifiers: .command)
                Button("模型供应商") { model.section = .models }
                    .keyboardShortcut("9", modifiers: .command)
            }
        }

        MenuBarExtra("niq", systemImage: "square.grid.2x2") {
            MenuBarView()
                .environmentObject(model)
        }
        .menuBarExtraStyle(.menu)

        Settings {
            SettingsView()
                .environmentObject(model)
        }
    }
}

/// The menu bar entry point — the previous menu bar app's functionality, now
/// driving the same `AppModel` as the window.
struct MenuBarView: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        if model.gatewayRunning {
            Button("打开 WebUI") { openGateway() }
            Button("停止网关") { Task { await model.stopGateway() } }
        } else {
            Button("启动网关") { Task { await model.startGateway() } }
        }

        Divider()

        if model.projects.isEmpty {
            Text("没有项目")
        } else {
            ForEach(model.projects) { project in
                Menu("\(project.id) · \(project.running == true ? "运行中" : "已停止")") {
                    Button("打开 WebUI") { openProject(project) }
                        .disabled(project.running != true)
                    Button("启动") { Task { await model.projectAction("start", project.id) } }
                        .disabled(project.running == true)
                    Button("停止") { Task { await model.projectAction("stop", project.id) } }
                        .disabled(project.running != true)
                    Button("重启") { Task { await model.projectAction("restart", project.id) } }
                        .disabled(project.running != true)
                }
            }
        }

        Divider()

        Button("刷新") {
            Task {
                await model.refreshStatus()
                await model.loadProjects()
            }
        }
        Button("退出") { NSApp.terminate(nil) }
    }

    private func openGateway() { NSWorkspace.shared.open(model.gatewayURL) }

    private func openProject(_ project: ProjectView) {
        if let url = URL(string: "/p/\(project.id)/", relativeTo: model.gatewayURL) {
            NSWorkspace.shared.open(url)
        }
    }
}
