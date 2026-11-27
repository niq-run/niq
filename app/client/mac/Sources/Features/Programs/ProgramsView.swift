import SwiftUI

/// Programs: the project's reusable instruction documents.
///
/// WebUI parity — a list (name · form · content type · description · contents),
/// a detail pane rendering the entry body as Markdown, and create / edit /
/// delete through the program worker (`PUT|DELETE /api/programs/{name}`).
struct ProgramsView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var selected: String?
        @Published var detail: ProgramDetail?
        @Published var query = ""
        @Published var editing = false
        @Published var creatingNew = false
    }

    private var rows: [ProgramInfo] {
        let needle = state.query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !needle.isEmpty else { return model.programs }
        return model.programs.filter {
            $0.name.lowercased().contains(needle)
                || ($0.description ?? "").lowercased().contains(needle)
                || ($0.tags ?? []).joined(separator: " ").lowercased().contains(needle)
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "程序", count: model.programs.count) {
                HStack(spacing: 8) {
                    HStack(spacing: 6) {
                        Image(systemName: "magnifyingglass").font(.system(size: 10)).foregroundStyle(Niq.textFaint)
                        TextField("搜索程序…", text: $state.query)
                            .textFieldStyle(.plain)
                            .font(.system(size: Niq.caption))
                            .frame(width: 170)
                    }
                    .padding(.horizontal, 9)
                    .padding(.vertical, 5)
                    .background(Niq.field.opacity(0.5))
                    .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))

                    Pill(title: "新建") { state.creatingNew = true; state.editing = true }
                    Pill(title: "刷新") { Task { await model.loadPrograms() } }
                }
            }

            HSplitView {
                list.frame(minWidth: 380, idealWidth: 520)
                detail.frame(minWidth: 360, idealWidth: 520)
            }
        }
        .background(Niq.window)
        .task { await model.loadPrograms() }
        .sheet(isPresented: $state.editing) {
            ProgramEditor(name: state.selected,
                          detail: state.creatingNew ? nil : state.detail,
                          isNew: state.creatingNew)
                .environmentObject(model)
                .frame(minWidth: 720, minHeight: 640)
        }
        .onChange(of: state.selected) { _, name in
            state.detail = nil
            guard let name else { return }
            Task { state.detail = await model.programDetail(name) }
        }
    }

    // MARK: List

    private var list: some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                Text("名称").frame(width: 170, alignment: .leading)
                Text("表单").frame(width: 72, alignment: .leading)
                Text("类型").frame(width: 82, alignment: .leading)
                Text("描述")
                Text("条").frame(width: 34, alignment: .trailing)
            }
            .font(.system(size: Niq.caption, weight: .medium))
            .foregroundStyle(Niq.textFaint)
            .padding(.horizontal, Niq.pagePadding)
            .padding(.vertical, 9)
            .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }

            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(rows) { program in
                        row(program)
                    }
                    if rows.isEmpty {
                        Text(model.programs.isEmpty ? "还没有程序" : "无匹配")
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                            .padding(.top, 40)
                    }
                }
            }
        }
    }

    private func row(_ program: ProgramInfo) -> some View {
        let selected = state.selected == program.name
        return HStack(spacing: 12) {
            Text(program.name)
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(selected ? Niq.accent : Niq.text)
                .lineLimit(1)
                .frame(width: 170, alignment: .leading)
            Text(formLabel(program.formType))
                .font(.system(size: 12))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 72, alignment: .leading)
            Text(contentLabel(program.contentType))
                .font(.system(size: 12))
                .foregroundStyle(Niq.textFaint)
                .lineLimit(1)
                .frame(width: 82, alignment: .leading)
            Text((program.description?.isEmpty == false ? program.description! : "—"))
                .font(.system(size: 12))
                .foregroundStyle(Niq.textFaint)
                .lineLimit(1)
            Text(program.contents.map(String.init) ?? "—")
                .font(Niq.mono(11))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 34, alignment: .trailing)
        }
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 8)
        .contentShape(Rectangle())
        .background(selected ? Niq.accentSoft.opacity(0.7) : Color.clear)
        .onTapGesture { state.selected = program.name }
    }

    // MARK: Detail

    @ViewBuilder
    private var detail: some View {
        if let detail = state.detail {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    HStack(spacing: 8) {
                        Text(detail.name)
                            .font(Niq.mono(Niq.body, weight: .semibold))
                        if detail.locked == true { Chip(text: "只读", color: Niq.pending) }
                        Spacer(minLength: 12)
                        Pill(title: "编辑", accent: true) { state.creatingNew = false; state.editing = true }
                        Pill(title: "删除") {
                            confirmDestructive("删除程序", "删除 \(detail.name) 及其子内容？") {
                                Task {
                                    await model.removeProgram(detail.name)
                                    state.selected = nil
                                    state.detail = nil
                                }
                            }
                        }
                    }

                    SectionBox(title: "概览") {
                        VStack(alignment: .leading, spacing: 6) {
                            FormRow(label: "表单") { Text(formLabel(detail.formType)).font(.system(size: 12)).foregroundStyle(Niq.textDim) }
                            FormRow(label: "内容类型") { Text(contentLabel(detail.contentType)).font(.system(size: 12)).foregroundStyle(Niq.textDim) }
                            FormRow(label: "描述") { Text(detail.description ?? "—").font(.system(size: 12)).foregroundStyle(Niq.textDim) }
                            FormRow(label: "标签") {
                                Text((detail.tags ?? []).isEmpty ? "—" : (detail.tags ?? []).joined(separator: " / "))
                                    .font(.system(size: 12))
                                    .foregroundStyle(Niq.textDim)
                            }
                            if let contents = detail.contents, !contents.isEmpty {
                                FormRow(label: "子内容") {
                                    VStack(alignment: .leading, spacing: 3) {
                                        ForEach(contents, id: \.self) { path in
                                            Text(path)
                                                .font(Niq.mono(11))
                                                .foregroundStyle(Niq.textFaint)
                                                .textSelection(.enabled)
                                        }
                                    }
                                }
                            }
                        }
                    }

                    SectionBox(title: "正文") {
                        MarkdownText(raw: detail.body, size: Niq.body)
                    }
                }
                .padding(Niq.pagePadding)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            VStack(spacing: 8) {
                Image(systemName: "text.book.closed")
                    .font(.system(size: 24, weight: .light))
                    .foregroundStyle(Niq.textFaint)
                Text("选择一个程序查看内容")
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textDim)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

// MARK: - Editor

private struct ProgramEditor: View {
    let name: String?
    let detail: ProgramDetail?
    let isNew: Bool

    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss

    @StateObject private var box = Box()
    private final class Box: ObservableObject {
        @Published var name = ""
        @Published var contentType = "playbook"
        @Published var description = ""
        @Published var tags = ""
        @Published var body = ""
        @Published var note = ""
        @Published var busy = false
    }

    private var titleText: String { isNew ? "新建程序" : "编辑程序" }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Text(titleText)
                    .font(.system(size: Niq.title, weight: .semibold))
                Spacer()
                if !box.note.isEmpty {
                    Text(box.note)
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.failed)
                }
                Pill(title: "取消") { dismiss() }
                Pill(title: "保存", accent: true) { save() }
            }
            .padding(Niq.pagePadding)
            .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }

            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    SectionBox(title: "元数据") {
                        VStack(alignment: .leading, spacing: 8) {
                            FormRow(label: "名称") {
                                if isNew {
                                    SquareField(text: $box.name, placeholder: "program-name", monospaced: true)
                                        .frame(width: 320)
                                } else {
                                    Text(name ?? "")
                                        .font(Niq.mono(12))
                                        .foregroundStyle(Niq.textDim)
                                }
                            }
                            FormRow(label: "内容类型") {
                                Menu {
                                    Button("手册（playbook）") { box.contentType = "playbook" }
                                    Button("指令（instruction）") { box.contentType = "instruction" }
                                } label: {
                                    Text(contentLabel(box.contentType))
                                        .font(.system(size: 12))
                                        .foregroundStyle(Niq.textDim)
                                }
                                .menuStyle(.borderlessButton)
                                .menuIndicator(.hidden)
                                .fixedSize()
                            }
                            FormRow(label: "描述") {
                                SquareField(text: $box.description, placeholder: "一句话说明").frame(width: 420)
                            }
                            FormRow(label: "标签") {
                                SquareField(text: $box.tags, placeholder: "逗号分隔").frame(width: 420)
                            }
                        }
                    }

                    SectionBox(title: "正文（Markdown）") {
                        SquareTextEditor(text: $box.body, minHeight: 320, monospaced: false,
                                         placeholder: "程序正文…")
                    }
                }
                .padding(Niq.pagePadding)
            }
        }
        .background(Niq.window)
        .onAppear(perform: load)
    }

    private func load() {
        guard let detail else { return }
        box.name = detail.name
        box.contentType = detail.contentType ?? "playbook"
        box.description = detail.description ?? ""
        box.tags = (detail.tags ?? []).joined(separator: ", ")
        box.body = detail.body
    }

    private func save() {
        let target = (isNew ? box.name : (name ?? "")).trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { box.note = "需要名称"; return }
        let tags = box.tags.split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        box.busy = true
        Task {
            let ok = await model.saveProgram(target, contentType: box.contentType,
                                             description: box.description, tags: tags, body: box.body)
            box.busy = false
            if ok { dismiss() } else { box.note = "保存失败" }
        }
    }
}

// The program vocabulary the WebUI uses (not MIME types).
func contentLabel(_ value: String?) -> String {
    switch value ?? "" {
    case "instruction": return "指令"
    case "playbook": return "手册"
    case "": return "—"
    default: return value!
    }
}

func formLabel(_ value: String?) -> String {
    switch value ?? "" {
    case "prompt": return "提示"
    case "script": return "脚本"
    case "": return "—"
    default: return value!
    }
}
