import SwiftUI
import AppKit

struct ContentView: View {
    @EnvironmentObject private var model: AppModel

    private var preferredScheme: ColorScheme? {
        switch model.appearance {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }

    var body: some View {
        HStack(spacing: 0) {
            SidebarView()
                .frame(width: Niq.sidebarWidth)
            Divider().opacity(0.6)
            DetailView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(minWidth: 1180, minHeight: 760)
        .background(Niq.window)
        .preferredColorScheme(preferredScheme)
        .sheet(item: $model.workerDetail) { ref in
            WorkerDetailView(workerID: ref.id)
                .environmentObject(model)
                .frame(minWidth: 620, idealWidth: 720, minHeight: 520, idealHeight: 640)
        }
        .overlay(alignment: .bottom) {
            if let error = model.errorMessage {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.system(size: 10))
                    Text(error).font(.system(size: 11.5)).lineLimit(2)
                }
                .foregroundStyle(.white)
                .padding(.horizontal, 12)
                .padding(.vertical, 7)
                .background(Niq.failed.opacity(0.92), in: Capsule())
                .padding(.bottom, 18)
                .onTapGesture { model.errorMessage = nil }
            }
        }
    }
}

// MARK: - Sidebar

private final class SidebarState: ObservableObject {
    @Published var search = ""
    @Published var showAll = false
    @Published var showPicker = false
}

private struct SidebarView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = SidebarState()

    var body: some View {
        VStack(spacing: 0) {
            brand
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    navSection(title: "项目视图", sections: AppModel.Section.primary)
                    hairline
                    navSection(title: "项目资源", sections: AppModel.Section.projectResources)
                    hairline
                    workerSelector
                    hairline
                    viewSettings
                    hairline
                    navSection(title: "资源", sections: AppModel.Section.resources)
                    hairline
                    projectSection
                }
                .padding(.bottom, 12)
            }
            footer
        }
        .background(Niq.sidebar)
        .sheet(isPresented: $state.showPicker) {
            WorkerPickerSheet()
                .environmentObject(model)
                .frame(minWidth: 420, minHeight: 460)
        }
    }

    private var hairline: some View {
        Rectangle().fill(Niq.borderSoft).frame(height: 1).padding(.vertical, 14)
    }

    // MARK: Brand

    private var brand: some View {
        HStack(spacing: 10) {
            Text("niq")
                .font(.system(size: 29, weight: .heavy, design: .rounded))
                .foregroundStyle(Niq.text)
            Spacer()
            Dot(color: model.gatewayRunning ? Niq.completed : Niq.neutral, size: 8)
            Button {
                Task {
                    await model.refreshStatus()
                    await model.loadProjects()
                    await model.loadProviders()
                    await model.connectSelected()
                }
            } label: {
                Image(systemName: "arrow.clockwise")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(Niq.textDim)
                    .padding(3)
            }
            .buttonStyle(.plain)
            .help("刷新 / 重连")
        }
        .padding(.horizontal, 18)
        .frame(height: Niq.headerHeight)
        .overlay(alignment: .bottom) {
            Rectangle().fill(Niq.borderSoft).frame(height: 1)
        }
    }

    // MARK: Navigation

    private func navSection(title: String, sections: [AppModel.Section]) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
                .font(.system(size: 14.5, weight: .semibold))
                .foregroundStyle(Niq.text)
                .padding(.horizontal, 18)
                .padding(.bottom, 8)
            ForEach(sections) { section in
                navRow(section)
            }
        }
        .padding(.horizontal, 10)
    }

    private func navRow(_ section: AppModel.Section) -> some View {
        Button {
            model.section = section
        } label: {
            HStack(spacing: 8) {
                Text(section.title)
                    .font(.system(size: 14))
                    .foregroundStyle(model.section == section ? Niq.accent : Niq.text)
                if model.section == section {
                    Circle().fill(Niq.accent).frame(width: 6, height: 6)
                }
                Spacer(minLength: 0)
                if let count = badge(for: section) {
                    Text("\(count)")
                        .font(Niq.mono(Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                }
            }
            .padding(.vertical, 8)
            .padding(.horizontal, 10)
            .contentShape(Rectangle())
            .background(
                model.section == section ? Niq.accentSoft : Color.clear,
                in: Rectangle()
            )
        }
        .buttonStyle(.plain)
    }

    private func badge(for section: AppModel.Section) -> Int? {
        switch section {
        case .workers: return model.workers.count
        case .projects: return model.projects.count
        default: return nil
        }
    }

    // MARK: Worker selector (event filter)

    private var visibleWorkers: [Worker] {
        let base = state.showAll ? model.workers : model.talkPartners
        let needle = state.search.trimmingCharacters(in: .whitespaces).lowercased()
        let filtered = needle.isEmpty ? base : base.filter {
            $0.id.lowercased().contains(needle)
                || ($0.label ?? "").lowercased().contains(needle)
                || ($0.type ?? "").lowercased().contains(needle)
                || ($0.description ?? "").lowercased().contains(needle)
        }
        return filtered.sorted { lhs, rhs in
            if (lhs.online ?? false) != (rhs.online ?? false) { return (lhs.online ?? false) }
            return lhs.displayName.localizedStandardCompare(rhs.displayName) == .orderedAscending
        }
    }

    private var workerSelector: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("工作单元选择")
                .font(.system(size: 14.5, weight: .semibold))
                .foregroundStyle(Niq.text)

            HStack(spacing: 7) {
                HStack(spacing: 6) {
                    Image(systemName: "magnifyingglass")
                        .font(.system(size: 11))
                        .foregroundStyle(Niq.textFaint)
                    TextField("搜索", text: $state.search)
                        .textFieldStyle(.plain)
                        .font(.system(size: 13))
                }
                .padding(.horizontal, 9)
                .padding(.vertical, 6)
                .background(Niq.field.opacity(0.5), in: Rectangle())
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )

                sideButton("展开") { state.showPicker = true }
                sideButton(state.showAll ? "只看对话" : "查看全部") { state.showAll.toggle() }
            }

            if model.viewFilter.isEmpty == false {
                HStack(spacing: 8) {
                    Text("已筛选 \(model.viewFilter.count) 个")
                        .font(.system(size: Niq.micro))
                        .foregroundStyle(Niq.textFaint)
                    Button("清除") { model.viewFilter = [] }
                        .buttonStyle(.plain)
                        .font(.system(size: Niq.micro))
                        .foregroundStyle(Niq.accent)
                    Spacer(minLength: 0)
                }
            }

            ScrollView {
                LazyVStack(alignment: .leading, spacing: 2) {
                    if visibleWorkers.isEmpty {
                        Text(model.workers.isEmpty ? "无在线工作单元" : "无匹配")
                            .font(.system(size: 12.5))
                            .foregroundStyle(Niq.textFaint)
                            .padding(.vertical, 10)
                    }
                    ForEach(visibleWorkers) { worker in
                        workerRow(worker)
                    }
                }
            }
            .frame(maxHeight: 248)
        }
        .padding(.horizontal, 18)
    }

    private func sideButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 12.5))
                .foregroundStyle(Niq.textDim)
                .padding(.horizontal, 9)
                .padding(.vertical, 6)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
        }
        .buttonStyle(.plain)
    }

    private func workerRow(_ worker: Worker) -> some View {
        let checked = model.viewFilter.contains(worker.id)
        return HStack(spacing: 8) {
            Button {
                model.toggleViewFilter(worker.id)
            } label: {
                Image(systemName: checked ? "checkmark.square.fill" : "square")
                    .font(.system(size: 12.5))
                    .foregroundStyle(checked ? Niq.accent : Niq.textFaint)
            }
            .buttonStyle(.plain)

            Dot(color: worker.online == true ? Niq.completed : Niq.neutral, size: 6)

            Text(worker.displayName)
                .font(.system(size: 13.5))
                .foregroundStyle(worker.online == true ? Niq.text : Niq.textDim)
                .lineLimit(1)

            if worker.hasRename {
                Text(worker.id)
                    .font(Niq.mono(Niq.micro))
                    .foregroundStyle(Niq.textFaint)
                    .lineLimit(1)
            }

            Spacer(minLength: 4)

            if let type = worker.type, !type.isEmpty {
                Text(type)
                    .font(Niq.mono(10.5))
                    .foregroundStyle(Niq.textFaint)
                    .lineLimit(1)
            }

            Button {
                model.workerDetail = WorkerRef(id: worker.id)
            } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 12))
                    .foregroundStyle(Niq.textFaint)
            }
            .buttonStyle(.plain)
            .help("操作")
            .contextMenu { workerMenu(worker) }
        }
        .padding(.vertical, 5)
        .padding(.horizontal, 5)
        .contentShape(Rectangle())
        .background(
            checked ? Niq.accentSoft.opacity(0.6) : Color.clear,
            in: Rectangle()
        )
        .contextMenu { workerMenu(worker) }
    }

    /// The WebUI's worker badge menu: mention, detail, filter, lifecycle.
    @ViewBuilder
    private func workerMenu(_ worker: Worker) -> some View {
        if worker.isTalkPartner {
            Button("提及") { model.toggleSendTarget(worker.id) }
        }
        Button("查看详情") { model.workerDetail = WorkerRef(id: worker.id) }
        Button(model.viewFilter.contains(worker.id) ? "取消筛选" : "只看此单元") {
            if model.viewFilter.contains(worker.id) { model.viewFilter.remove(worker.id) }
            else { model.viewFilter = [worker.id] }
        }
        if !worker.stateLabel.isEmpty, worker.canSuspend || worker.canResume {
            Divider()
            if worker.canSuspend {
                Button("休眠") { Task { await model.workerAction(worker.id, "suspend") } }
            }
            if worker.canResume {
                Button("唤醒") { Task { await model.workerAction(worker.id, "resume") } }
            }
        }
    }

    // MARK: View settings

    private var viewSettings: some View {
        VStack(alignment: .leading, spacing: 9) {
            Text("对话视图设置")
                .font(.system(size: 14.5, weight: .semibold))
                .foregroundStyle(Niq.text)
                .padding(.bottom, 3)
            settingToggle("流式模式", isOn: $model.streamingMode)
            settingToggle("展开思考", isOn: $model.expandThinking)
            settingToggle("折叠过程事件", isOn: $model.collapseProcess)
            settingToggle("仅回复", isOn: $model.responseOnly)
        }
        .padding(.horizontal, 18)
    }

    private func settingToggle(_ title: String, isOn: Binding<Bool>) -> some View {
        HStack(spacing: 10) {
            Text(title)
                .font(.system(size: 13.5))
                .foregroundStyle(Niq.text)
            Spacer(minLength: 10)
            Toggle("", isOn: isOn)
                .labelsHidden()
                .toggleStyle(.switch)
                .controlSize(.small)
        }
        .frame(height: 26)
    }

    // MARK: Project

    private var projectSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("当前项目")
                .font(.system(size: 14.5, weight: .semibold))
                .foregroundStyle(Niq.text)

            Menu {
                if model.projects.isEmpty {
                    Text("没有项目")
                } else {
                    ForEach(model.projects) { project in
                        Button {
                            model.selectProject(project.id)
                        } label: {
                            Text("\(project.id) · \(project.running == true ? "运行中" : "已停止")")
                        }
                    }
                }
            } label: {
                HStack(spacing: 7) {
                    Dot(color: model.selectedProject?.running == true ? Niq.completed : Niq.neutral, size: 7)
                    Text(model.selectedProjectID ?? "选择项目")
                        .font(.system(size: 13.5))
                        .foregroundStyle(Niq.text)
                    Spacer(minLength: 6)
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.system(size: 9))
                        .foregroundStyle(Niq.textFaint)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 7)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .disabled(model.busy)

            HStack(spacing: 7) {
                projectButton("启动", "start", enabled: model.selectedProject?.running != true)
                projectButton("停止", "stop", enabled: model.selectedProject?.running == true)
                projectButton("重启", "restart", enabled: model.selectedProject?.running == true)
            }
        }
        .padding(.horizontal, 18)
    }

    private func projectButton(_ title: String, _ verb: String, enabled: Bool) -> some View {
        Button {
            guard let id = model.selectedProjectID else { return }
            Task { await model.projectAction(verb, id) }
        } label: {
            Text(title)
                .font(.system(size: 12.5))
                .foregroundStyle(enabled ? Niq.textDim : Niq.textFaint.opacity(0.6))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 6)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
    }

    // MARK: Footer

    private var footer: some View {
        HStack(spacing: 8) {
            Text(model.selectedProjectID ?? "—")
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textDim)
            Spacer()
            Menu {
                ForEach(AppearanceMode.allCases) { mode in
                    Button(mode.label + (model.appearance == mode ? "  ✓" : "")) {
                        model.appearance = mode
                    }
                }
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: model.appearance == .dark ? "moon.fill" : "circle.lefthalf.filled")
                        .font(.system(size: 9.5))
                    Text(model.appearance.shortLabel)
                        .font(.system(size: Niq.micro))
                }
                .foregroundStyle(Niq.textDim)
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
            .help("外观")

            Text(model.gatewayRunning ? "在线" : "离线")
                .font(.system(size: Niq.micro))
                .foregroundStyle(model.gatewayRunning ? Niq.completed : Niq.textFaint)
        }
        .padding(.horizontal, 18)
        .frame(height: 42)
        .overlay(alignment: .top) {
            Rectangle().fill(Niq.borderSoft).frame(height: 1)
        }
    }
}

// MARK: - Worker picker sheet (查看全部 / 展开)

private struct WorkerPickerSheet: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @StateObject private var state = Box()

    private final class Box: ObservableObject { @Published var search = "" }

    private var workers: [Worker] {
        let needle = state.search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !needle.isEmpty else { return model.workers }
        return model.workers.filter {
            $0.id.lowercased().contains(needle)
                || ($0.label ?? "").lowercased().contains(needle)
                || ($0.type ?? "").lowercased().contains(needle)
                || ($0.description ?? "").lowercased().contains(needle)
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Text("工作单元选择")
                    .font(.system(size: 14, weight: .semibold))
                Spacer()
                Button("完成") { dismiss() }
                    .buttonStyle(.plain)
                    .font(.system(size: 12))
                    .foregroundStyle(Niq.accent)
            }
            .padding(16)
            .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }

            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .font(.system(size: 11))
                    .foregroundStyle(Niq.textFaint)
                TextField("搜索名称、标签、描述…", text: $state.search)
                    .textFieldStyle(.plain)
                    .font(.system(size: 12.5))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(Niq.field.opacity(0.5), in: Rectangle())
            .overlay(
                Rectangle()
                    .strokeBorder(Niq.borderSoft, lineWidth: 1)
            )
            .padding(16)

            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(workers) { worker in
                        HStack(spacing: 10) {
                            Button {
                                model.toggleViewFilter(worker.id)
                            } label: {
                                Image(systemName: model.viewFilter.contains(worker.id) ? "checkmark.square.fill" : "square")
                                    .font(.system(size: 13))
                                    .foregroundStyle(model.viewFilter.contains(worker.id) ? Niq.accent : Niq.textFaint)
                            }
                            .buttonStyle(.plain)

                            Dot(color: worker.online == true ? Niq.completed : Niq.neutral, size: 7)

                            VStack(alignment: .leading, spacing: 1) {
                                Text(worker.displayName)
                                    .font(.system(size: 13))
                                if let description = worker.description, !description.isEmpty {
                                    Text(description)
                                        .font(.system(size: 11))
                                        .foregroundStyle(Niq.textFaint)
                                        .lineLimit(1)
                                }
                            }

                            Spacer(minLength: 8)

                            if let type = worker.type {
                                Chip(text: type, color: Niq.textDim)
                            }
                            Button {
                                // The picker is itself a sheet: dismiss it first,
                                // then present the detail on the next runloop.
                                dismiss()
                                DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) {
                                    model.workerDetail = WorkerRef(id: worker.id)
                                }
                            } label: {
                                Image(systemName: "info.circle")
                                    .font(.system(size: 12))
                                    .foregroundStyle(Niq.textFaint)
                            }
                            .buttonStyle(.plain)
                            .help("查看详情")
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 7)
                        .contentShape(Rectangle())
                        .background(
                            model.viewFilter.contains(worker.id) ? Niq.accentSoft.opacity(0.5) : Color.clear
                        )
                        Rectangle().fill(Niq.borderSoft).frame(height: 0.5).padding(.leading, 16)
                    }
                }
            }

            HStack {
                Text("勾选的工作单元构成对话的事件筛选；发送对象在输入框左侧选择。")
                    .font(.system(size: 10.5))
                    .foregroundStyle(Niq.textFaint)
                Spacer()
                Button("清除筛选") { model.viewFilter = [] }
                    .buttonStyle(.plain)
                    .font(.system(size: 11))
                    .foregroundStyle(Niq.accent)
            }
            .padding(16)
            .overlay(alignment: .top) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
        }
        .background(Niq.window)
    }
}

// MARK: - Detail router

private struct DetailView: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        Group {
            switch model.section {
            case .talk:
                if model.selectedProjectID == nil { noProject } else { TalkView() }
            case .events:
                if model.selectedProjectID == nil { noProject } else { EventsView() }
            case .approvals:
                if model.selectedProjectID == nil { noProject } else { ApprovalsView() }
            case .mcp:
                if model.selectedProjectID == nil { noProject } else { McpView() }
            case .programs:
                if model.selectedProjectID == nil { noProject } else { ProgramsView() }
            case .templates:
                TemplatesView()
            case .workers:
                if model.selectedProjectID == nil { noProject } else { WorkersView() }
            case .projects:
                ProjectsView()
            case .models:
                ModelsView()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var noProject: some View {
        VStack(spacing: 10) {
            Image(systemName: "square.grid.2x2")
                .font(.system(size: 26, weight: .light))
                .foregroundStyle(Niq.textFaint)
            Text("没有选中的项目").font(.system(size: 13)).foregroundStyle(Niq.textDim)
            Text("在左侧“资源”里选一个项目，或先启动它。")
                .font(.system(size: 11.5))
                .foregroundStyle(Niq.textFaint)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Niq.window)
    }
}
