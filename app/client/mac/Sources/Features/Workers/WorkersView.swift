import SwiftUI

/// The roster of the selected project's bus, in the WebUI's table shape:
/// id · 备注名 · 类型 · 描述 · 连接 · 生命周期 · 托管.
struct WorkersView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var search = ""
        @Published var typeFilter = ""
        @Published var showArchived = false
        @Published var creating = false
    }

    private var types: [String] {
        Array(Set(model.workers.compactMap { $0.type })).sorted()
    }

    private var rows: [Worker] {
        let needle = state.search.trimmingCharacters(in: .whitespaces).lowercased()
        return model.workers.filter { worker in
            if !state.typeFilter.isEmpty, worker.type != state.typeFilter { return false }
            if !state.showArchived, model.isArchived(worker.id) { return false }
            guard !needle.isEmpty else { return true }
            return worker.id.lowercased().contains(needle)
                || (worker.label ?? "").lowercased().contains(needle)
                || (worker.type ?? "").lowercased().contains(needle)
                || (worker.description ?? "").lowercased().contains(needle)
        }
        .sorted { lhs, rhs in
            if (lhs.online ?? false) != (rhs.online ?? false) { return (lhs.online ?? false) }
            return lhs.id.localizedStandardCompare(rhs.id) == .orderedAscending
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "工作单元", count: model.workers.count) {
                HStack(spacing: 8) {
                    HStack(spacing: 6) {
                        Image(systemName: "magnifyingglass")
                            .font(.system(size: 10))
                            .foregroundStyle(Niq.textFaint)
                        TextField("搜索工作单元…", text: $state.search)
                            .textFieldStyle(.plain)
                            .font(.system(size: Niq.caption))
                            .frame(width: 190)
                    }
                    .padding(.horizontal, 9)
                    .padding(.vertical, 5)
                    .background(Niq.field.opacity(0.5))
                    .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))

                    Menu {
                        Button("全部类型") { state.typeFilter = "" }
                        Divider()
                        ForEach(types, id: \.self) { type in
                            Button(type) { state.typeFilter = type }
                        }
                    } label: {
                        Text(state.typeFilter.isEmpty ? "类型: 全部" : "类型: \(state.typeFilter)")
                            .font(.system(size: 12))
                            .foregroundStyle(Niq.textDim)
                    }
                    .menuStyle(.borderlessButton)
                    .menuIndicator(.hidden)
                    .fixedSize()

                    Toggle(isOn: $state.showArchived) {
                        Text("显示已归档").font(.system(size: Niq.caption))
                    }
                    .toggleStyle(.checkbox)
                    .controlSize(.small)

                    Pill(title: "创建工作单元", accent: true) { state.creating = true }
                    Pill(title: "刷新") { Task { await model.reloadWorkers(); await model.loadArchived() } }
                }
            }

            tableHeader
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    ForEach(rows) { worker in
                        row(worker)
                    }
                    if rows.isEmpty {
                        Text("没有匹配的工作单元")
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
            CreateWorkerDialog()
                .environmentObject(model)
                .frame(minWidth: 760, minHeight: 700)
        }
    }

    private var tableHeader: some View {
        HStack(spacing: 14) {
            Text("工作单元 ID").frame(width: 168, alignment: .leading)
            Text("备注名").frame(width: 100, alignment: .leading)
            Text("类型").frame(width: 100, alignment: .leading)
            Text("描述")
            Text("连接").frame(width: 60, alignment: .leading)
            Text("生命周期").frame(width: 74, alignment: .leading)
            Text("托管").frame(width: 60, alignment: .leading)
        }
        .font(.system(size: Niq.caption, weight: .medium))
        .foregroundStyle(Niq.textFaint)
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 10)
        .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
    }

    private func row(_ worker: Worker) -> some View {
        HStack(spacing: 14) {
            HStack(spacing: 6) {
                Text(worker.id)
                    .font(Niq.mono(Niq.caption))
                    .foregroundStyle(Niq.text)
                    .lineLimit(1)
                if model.isArchived(worker.id) {
                    Chip(text: "已归档", color: Niq.textFaint)
                }
            }
            .frame(width: 168, alignment: .leading)

            Text(worker.hasRename ? worker.label! : "—")
                .font(.system(size: Niq.caption))
                .foregroundStyle(worker.hasRename ? Niq.textDim : Niq.textFaint)
                .lineLimit(1)
                .frame(width: 100, alignment: .leading)

            typeTag(worker.type ?? "—")
                .frame(width: 100, alignment: .leading)

            Text((worker.description?.isEmpty == false ? worker.description! : "—"))
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .lineLimit(1)

            HStack(spacing: 5) {
                Dot(color: worker.online == true ? Niq.completed : Niq.neutral, size: 6)
                Text(worker.online == true ? "在线" : "离线")
                    .font(.system(size: 12))
                    .foregroundStyle(worker.online == true ? Niq.textDim : Niq.textFaint)
            }
            .frame(width: 60, alignment: .leading)

            Text(worker.stateLabel)
                .font(.system(size: 12))
                .foregroundStyle(Niq.textDim)
                .frame(width: 74, alignment: .leading)

            Text(worker.placementLabel)
                .font(.system(size: 12))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 60, alignment: .leading)
        }
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 9)
        .contentShape(Rectangle())
        .onTapGesture { model.workerDetail = WorkerRef(id: worker.id) }
        .contextMenu {
            Button("查看详情") { model.workerDetail = WorkerRef(id: worker.id) }
            Button(model.isArchived(worker.id) ? "取消归档" : "归档") {
                Task { await model.setArchived(worker.id, !model.isArchived(worker.id)) }
            }
        }
    }

    private func typeTag(_ type: String) -> some View {
        let color: Color
        switch type {
        case "reason": color = Niq.reason
        case "coding": color = Color(red: 0.36, green: 0.6, blue: 0.3)
        case "niq", "mcp": color = Niq.accent
        default: color = Niq.textDim
        }
        return Text(type)
            .font(Niq.mono(Niq.caption))
            .foregroundStyle(color)
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(color.opacity(0.10))
    }
}
