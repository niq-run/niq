import SwiftUI

/// Shared resources: the project catalog with start / stop / restart.
struct ProjectsView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var creating = false
        @Published var linking = false
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            tableHeader
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    ForEach(model.projects) { project in
                        row(project)
                    }
                    if model.projects.isEmpty {
                        Text("没有项目")
                            .font(.system(size: 12))
                            .foregroundStyle(Niq.textFaint)
                            .frame(maxWidth: .infinity)
                            .padding(.top, 40)
                    }
                }
            }
        }
        .background(Niq.window)
        .sheet(isPresented: $state.creating) {
            CreateProjectSheet()
                .environmentObject(model)
                .frame(minWidth: 460, minHeight: 320)
        }
        .sheet(isPresented: $state.linking) {
            LinkProjectSheet()
                .environmentObject(model)
                .frame(minWidth: 560, minHeight: 460)
        }
    }

    private var header: some View {
        PageHeader(title: "项目", count: model.projects.count) {
            HStack(spacing: 8) {
                Pill(title: "新建", accent: true) { state.creating = true }
                Pill(title: "链接目录") { state.linking = true }
                Pill(title: "刷新") { Task { await model.loadProjects() } }
            }
        }
    }

    private var tableHeader: some View {
        HStack(spacing: 14) {
            Text("项目 ID").frame(width: 180, alignment: .leading)
            Text("目录")
            Text("端口").frame(width: 130, alignment: .leading)
            Text("状态").frame(width: 70, alignment: .leading)
            Text("操作").frame(width: 170, alignment: .leading)
        }
        .font(.system(size: Niq.caption, weight: .medium))
        .foregroundStyle(Niq.textFaint)
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 10)
        .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
    }

    private func row(_ project: ProjectView) -> some View {
        let selected = model.selectedProjectID == project.id
        return HStack(spacing: 14) {
            HStack(spacing: 7) {
                Text(project.id)
                    .font(Niq.mono(Niq.caption))
                    .foregroundStyle(selected ? Niq.accent : Niq.text)
                if selected {
                    Text("当前")
                        .font(.system(size: 10))
                        .foregroundStyle(Niq.accent)
                        .padding(.horizontal, 5)
                        .padding(.vertical, 1)
                        .background(Niq.accentSoft, in: Rectangle())
                }
            }
            .frame(width: 180, alignment: .leading)

            Text(project.dir ?? "—")
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .lineLimit(1)
                .truncationMode(.middle)

            Text(portsText(project))
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 130, alignment: .leading)

            HStack(spacing: 6) {
                Dot(color: project.running == true ? Niq.completed : Niq.neutral, size: 6)
                Text(project.running == true ? "运行中" : "已停止")
                    .font(.system(size: 12))
                    .foregroundStyle(Niq.textDim)
            }
            .frame(width: 70, alignment: .leading)

            HStack(spacing: 7) {
                projectAction("启动", "start", enabled: project.running != true, project: project)
                projectAction("停止", "stop", enabled: project.running == true, project: project)
                projectAction("重启", "restart", enabled: project.running == true, project: project)
            }
            .frame(width: 170, alignment: .leading)
        }
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 10)
        .contentShape(Rectangle())
        .background(selected ? Niq.accentSoft.opacity(0.6) : Color.clear)
        .onTapGesture { model.selectProject(project.id) }
    }

    private func portsText(_ project: ProjectView) -> String {
        var parts: [String] = []
        if let bus = project.ports?.bus { parts.append("bus \(bus)") }
        if let webui = project.ports?.webui { parts.append("web \(webui)") }
        return parts.isEmpty ? "—" : parts.joined(separator: " · ")
    }

    private func projectAction(_ title: String, _ verb: String, enabled: Bool, project: ProjectView) -> some View {
        Button {
            Task { await model.projectAction(verb, project.id) }
        } label: {
            Text(title)
                .font(.system(size: 11))
                .foregroundStyle(enabled ? Niq.textDim : Niq.textFaint.opacity(0.6))
                .padding(.horizontal, 8)
                .padding(.vertical, 3)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
        }
        .buttonStyle(.plain)
        .disabled(!enabled || model.busy)
    }
}

// MARK: - Create from template

private struct CreateProjectSheet: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @StateObject private var box = Box()

    private final class Box: ObservableObject {
        @Published var id = ""
        @Published var template = ""
        @Published var note = ""
        @Published var busy = false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("新建项目")
                .font(.system(size: Niq.title, weight: .semibold))

            FormRow(label: "项目 ID") {
                SquareField(text: $box.id, placeholder: "my-project", monospaced: true)
                    .frame(width: 260)
            }
            FormRow(label: "模板") {
                Menu {
                    if model.templates.isEmpty {
                        Text("没有模板")
                    } else {
                        ForEach(model.templates, id: \.self) { name in
                            Button(name) { box.template = name }
                        }
                    }
                } label: {
                    Text(box.template.isEmpty ? "选择模板" : box.template)
                        .font(Niq.mono(Niq.caption))
                        .foregroundStyle(box.template.isEmpty ? Niq.textFaint : Niq.textDim)
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
            }

            if !box.note.isEmpty {
                Text(box.note).font(.system(size: Niq.caption)).foregroundStyle(Niq.failed)
            }

            Spacer(minLength: 0)

            HStack(spacing: 8) {
                Spacer()
                Pill(title: "取消") { dismiss() }
                Pill(title: "创建并启动", accent: true, enabled: !box.busy) { create() }
            }
        }
        .padding(Niq.pagePadding)
        .background(Niq.window)
        .task { if model.templates.isEmpty { await model.loadTemplates() } }
    }

    private func create() {
        let id = box.id.trimmingCharacters(in: .whitespaces)
        guard !id.isEmpty, !box.template.isEmpty else {
            box.note = "需要项目 ID 与模板"
            return
        }
        box.busy = true
        Task {
            let ok = await model.createProject(id: id, template: box.template)
            box.busy = false
            if ok { dismiss() } else { box.note = "创建失败（见底部提示）" }
        }
    }
}

// MARK: - Link a directory

private struct LinkProjectSheet: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @StateObject private var box = Box()

    private final class Box: ObservableObject {
        @Published var path = ""
        @Published var entries: [GatewayClient.DirEntry] = []
        @Published var note = ""
        @Published var busy = false
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Text("链接已有目录")
                    .font(.system(size: Niq.title, weight: .semibold))
                Spacer()
                if !box.note.isEmpty {
                    Text(box.note).font(.system(size: Niq.caption)).foregroundStyle(Niq.failed)
                }
                Pill(title: "取消") { dismiss() }
                Pill(title: "链接此目录", accent: true, enabled: !box.busy) { link() }
            }
            .padding(Niq.pagePadding)
            .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }

            HStack(spacing: 8) {
                Text(box.path.isEmpty ? "~niq 根目录" : box.path)
                    .font(Niq.mono(Niq.caption))
                    .foregroundStyle(Niq.textDim)
                    .lineLimit(1)
                Spacer(minLength: 8)
                if !box.path.isEmpty {
                    Pill(title: "上一级") { Task { await browse(parent(of: box.path)) } }
                }
            }
            .padding(.horizontal, Niq.pagePadding)
            .padding(.vertical, 10)

            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(box.entries) { entry in
                        HStack(spacing: 8) {
                            Image(systemName: "folder")
                                .font(.system(size: 11))
                                .foregroundStyle(Niq.textFaint)
                            Text(entry.name)
                                .font(Niq.mono(Niq.caption))
                                .foregroundStyle(Niq.text)
                            Spacer(minLength: 8)
                            Image(systemName: "chevron.right")
                                .font(.system(size: 9))
                                .foregroundStyle(Niq.textFaint)
                        }
                        .padding(.horizontal, Niq.pagePadding)
                        .padding(.vertical, 8)
                        .contentShape(Rectangle())
                        .onTapGesture { Task { await browse(entry.path) } }
                        Rectangle().fill(Niq.borderSoft).frame(height: 0.5)
                    }
                    if box.entries.isEmpty {
                        Text("（无子目录）")
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                            .padding(.top, 24)
                    }
                }
            }
        }
        .background(Niq.window)
        .task { await browse("") }
    }

    private func parent(of path: String) -> String {
        var components = path.split(separator: "/").map(String.init)
        if components.isEmpty { return "" }
        components.removeLast()
        return components.isEmpty ? "" : "/" + components.joined(separator: "/")
    }

    private func browse(_ path: String) async {
        box.path = path
        box.entries = await model.projectDirs(path.isEmpty ? nil : path)
    }

    private func link() {
        guard !box.path.isEmpty else { box.note = "先进入目标目录"; return }
        box.busy = true
        Task {
            let ok = await model.linkProject(path: box.path)
            box.busy = false
            if ok { dismiss() } else { box.note = "链接失败（见底部提示）" }
        }
    }
}
