import SwiftUI
import AppKit
import UniformTypeIdentifiers

/// Project templates (control plane): the on-disk template catalog.
///
/// WebUI parity — list, view/edit the template JSON, create a new one (blank or
/// cloned from an existing template), import a shared .zip package, delete.
struct TemplatesView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var selected: String?
        @Published var json = ""
        @Published var note = ""
        @Published var saving = false
        @Published var creating = false
        @Published var dirty = false
    }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "模板", count: model.templates.count) {
                HStack(spacing: 8) {
                    Pill(title: "新建") { state.creating = true }
                    Pill(title: "导入") { importZip() }
                    Pill(title: "刷新") { Task { await model.loadTemplates() } }
                }
            }

            HSplitView {
                list.frame(minWidth: 240, idealWidth: 280, maxWidth: 360)
                editor.frame(minWidth: 420)
            }
        }
        .background(Niq.window)
        .task { await model.loadTemplates() }
        .sheet(isPresented: $state.creating) {
            TemplateCreator()
                .environmentObject(model)
                .frame(minWidth: 460, minHeight: 300)
        }
        .onChange(of: state.selected) { _, name in
            guard let name else { state.json = ""; return }
            Task {
                if let document = await model.templateDocument(name) {
                    state.json = document.jsonText(pretty: true)
                    state.note = ""
                    state.dirty = false
                }
            }
        }
    }

    // MARK: List

    private var list: some View {
        ScrollView {
            LazyVStack(spacing: 0) {
                ForEach(model.templates, id: \.self) { name in
                    let selected = state.selected == name
                    HStack(spacing: 8) {
                        Text(name)
                            .font(Niq.mono(Niq.caption))
                            .foregroundStyle(selected ? Niq.accent : Niq.text)
                            .lineLimit(1)
                        Spacer(minLength: 8)
                        if selected {
                            Circle().fill(Niq.accent).frame(width: 5, height: 5)
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 8)
                    .contentShape(Rectangle())
                    .background(selected ? Niq.accentSoft.opacity(0.7) : Color.clear)
                    .onTapGesture { state.selected = name }
                    .contextMenu {
                        Button("删除") {
                            confirmDestructive("删除模板", "删除模板 \(name)？") {
                                Task {
                                    await model.removeTemplate(name)
                                    if state.selected == name { state.selected = nil }
                                }
                            }
                        }
                    }
                }
                if model.templates.isEmpty {
                    Text("还没有模板")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                        .padding(.top, 40)
                }
            }
        }
    }

    // MARK: Editor

    @ViewBuilder
    private var editor: some View {
        if let name = state.selected {
            VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Text(name)
                        .font(Niq.mono(Niq.caption, weight: .semibold))
                    Spacer(minLength: 12)
                    if !state.note.isEmpty {
                        Text(state.note)
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(state.note == "已保存" ? Niq.completed : Niq.failed)
                    }
                    Pill(title: "保存", accent: true, enabled: !state.saving) { save(name) }
                    Pill(title: "删除") {
                        confirmDestructive("删除模板", "删除模板 \(name)？") {
                            Task {
                                await model.removeTemplate(name)
                                state.selected = nil
                            }
                        }
                    }
                }
                .padding(.horizontal, Niq.pagePadding)
                .padding(.vertical, 10)
                .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }

                SquareTextEditor(text: $state.json, minHeight: 480, monospaced: true,
                                 placeholder: "{ … }")
                    .padding(Niq.pagePadding)
            }
        } else {
            VStack(spacing: 8) {
                Image(systemName: "square.on.square")
                    .font(.system(size: 24, weight: .light))
                    .foregroundStyle(Niq.textFaint)
                Text("选择一个模板查看 JSON")
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textDim)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: Actions

    private func save(_ name: String) {
        let (document, error) = parseJSON(state.json)
        guard let document else {
            state.note = error ?? "JSON 无效"
            return
        }
        state.saving = true
        Task {
            let ok = await model.saveTemplate(id: name, document: document, existing: true)
            state.saving = false
            state.note = ok ? "已保存" : "保存失败"
        }
    }

    private func importZip() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.zip]
        panel.allowsMultipleSelection = false
        panel.prompt = "导入"
        guard panel.runModal() == .OK, let url = panel.urls.first else { return }
        Task { _ = await model.importTemplate(id: "", fileURL: url) }
    }
}

// MARK: - Create

private struct TemplateCreator: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @StateObject private var box = Box()

    private final class Box: ObservableObject {
        @Published var id = ""
        @Published var copyFrom = ""
        @Published var note = ""
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("新建模板")
                .font(.system(size: Niq.title, weight: .semibold))

            FormRow(label: "名称") {
                SquareField(text: $box.id, placeholder: "my-template", monospaced: true)
                    .frame(width: 260)
            }
            FormRow(label: "复制自") {
                Menu {
                    Button("空白") { box.copyFrom = "" }
                    Divider()
                    ForEach(model.templates, id: \.self) { name in
                        Button(name) { box.copyFrom = name }
                    }
                } label: {
                    Text(box.copyFrom.isEmpty ? "空白" : box.copyFrom)
                        .font(Niq.mono(Niq.caption))
                        .foregroundStyle(Niq.textDim)
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
                Pill(title: "创建", accent: true) { create() }
            }
        }
        .padding(Niq.pagePadding)
        .background(Niq.window)
    }

    private func create() {
        let id = box.id.trimmingCharacters(in: .whitespaces)
        guard !id.isEmpty else { box.note = "需要名称"; return }
        Task {
            let document: JSONValue
            if box.copyFrom.isEmpty {
                document = .object(["id": .string(id)])
            } else if let source = await model.templateDocument(box.copyFrom) {
                document = source
            } else {
                box.note = "读取模板失败"
                return
            }
            let ok = await model.saveTemplate(id: id, document: document, existing: false)
            if ok { dismiss() } else { box.note = "创建失败" }
        }
    }
}
