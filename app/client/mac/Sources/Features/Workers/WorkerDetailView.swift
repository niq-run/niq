import SwiftUI
import AppKit

/// One worker's full detail: identity & metadata, its self-description, context
/// & provider configuration, its recent events, and its bus permissions.
///
/// WebUI parity (the `WorkerDetail` panel): 信息 / 自述 / 上下文 / 事件 / 许可,
/// lifecycle in the header, plus 归档 and 删除.
struct WorkerDetailView: View {
    let workerID: String
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @StateObject private var box = Box()

    private final class Box: ObservableObject {
        @Published var tab = 0
        @Published var busy = false
        @Published var note = ""

        // meta editing
        @Published var editingMeta = false
        @Published var label = ""
        @Published var tags = ""
        @Published var description = ""

        // params editing
        @Published var editingParams = false
        @Published var paramsText = ""

        // readme
        @Published var readme = ""
        @Published var readmeError = ""
        @Published var readmeLoading = false
        @Published var readmeLoaded = false

        // provider
        @Published var providerName = ""
        @Published var modelName = ""

        // perms
        @Published var publish: [PublishPattern] = []
        @Published var subscribe: [EventPattern] = []
        @Published var permsLoaded = false

        // mounts
        @Published var newMount = ""
        @Published var mountNote = ""
    }

    private var worker: Worker? { model.worker(workerID) }
    private var providers: WorkerProviders? { model.workerProviders[workerID] }

    var body: some View {
        VStack(spacing: 0) {
            header
            SquareTabs(items: ["信息", "自述", "上下文", "事件", "许可"], selection: $box.tab)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    switch box.tab {
                    case 0: infoTab
                    case 1: readmeTab
                    case 2: contextTab
                    case 3: eventsTab
                    default: permsTab
                    }
                }
                .padding(Niq.pagePadding)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(Niq.window)
        .task {
            await model.loadWorkerProviders(workerID)
            await model.loadWorkerContext(workerID)
            await model.loadWorkerMounts(workerID)
            loadDrafts()
        }
        .onChange(of: box.tab) { _, tab in
            if tab == 1 && !box.readmeLoaded { loadReadme() }
        }
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: 8) {
            Text(workerID)
                .font(Niq.mono(Niq.title, weight: .semibold))
            if let type = worker?.type { Chip(text: type, color: Niq.textDim) }
            if let worker {
                Dot(color: worker.online == true ? Niq.completed : Niq.neutral, size: 7)
                Text(worker.stateLabel)
                    .font(.system(size: Niq.micro))
                    .foregroundStyle(Niq.textFaint)
            }
            if let note = box.note.isEmpty ? nil : box.note {
                Text(note)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textDim)
                    .lineLimit(1)
            }
            Spacer(minLength: 12)
            if let worker {
                if worker.canStart { action("启动", "start", worker) }
                if worker.canSuspend { action("休眠", "suspend", worker) }
                if worker.canResume { action("唤醒", "resume", worker) }
                action("重启", "restart", worker)
                if worker.canStop { action("停止", "stop", worker) }
                Pill(title: model.isArchived(worker.id) ? "取消归档" : "归档") {
                    Task { await model.setArchived(worker.id, !model.isArchived(worker.id)) }
                }
                Pill(title: "删除") {
                    confirmDestructive("删除工作单元", "永久移除 \(worker.id)？此操作不可撤销。") {
                        Task { await model.removeWorker(worker.id) }
                    }
                }
            }
            Button {
                dismiss()
            } label: {
                Image(systemName: "xmark")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(Niq.textDim)
            }
            .buttonStyle(.plain)
            .padding(.leading, 4)
        }
        .padding(.horizontal, Niq.pagePadding)
        .frame(height: Niq.headerHeight)
        .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
    }

    private func action(_ title: String, _ verb: String, _ worker: Worker) -> some View {
        Pill(title: title, enabled: !box.busy) {
            Task {
                box.busy = true
                await model.workerAction(worker.id, verb)
                box.busy = false
                box.note = "已请求 \(title) \(worker.id)"
            }
        }
    }

    // MARK: 信息

    private var infoTab: some View {
        VStack(alignment: .leading, spacing: 14) {
            SectionBox(title: "标签与描述") {
                VStack(alignment: .leading, spacing: 8) {
                    if box.editingMeta {
                        FormRow(label: "备注名") { SquareField(text: $box.label, placeholder: "显示名称") }
                        FormRow(label: "标签") { SquareField(text: $box.tags, placeholder: "逗号分隔") }
                        FormRow(label: "描述") { SquareField(text: $box.description, placeholder: "一句话说明") }
                        HStack(spacing: 8) {
                            Spacer()
                            Pill(title: "取消") { box.editingMeta = false }
                            Pill(title: "保存", accent: true) { saveMeta() }
                        }
                    } else {
                        HStack(alignment: .top, spacing: 12) {
                            VStack(alignment: .leading, spacing: 6) {
                                field("ID", workerID)
                                field("类型", worker?.type ?? "—")
                                field("备注名", worker?.hasRename == true ? worker!.label! : "—")
                                field("描述", worker?.description ?? "—")
                                field("连接", worker?.online == true ? "在线" : "离线")
                                field("生命周期", worker?.stateLabel ?? "—")
                                field("托管", worker?.placementLabel ?? "—")
                                field("标签", (worker?.tags ?? []).isEmpty ? "—" : (worker!.tags ?? []).joined(separator: " / "))
                            }
                            Spacer(minLength: 12)
                            Pill(title: "编辑") { beginMeta() }
                        }
                    }
                    if let credential = worker?.credential, !credential.isEmpty {
                        HStack(alignment: .top, spacing: 8) {
                            Text("凭证")
                                .font(.system(size: Niq.caption))
                                .foregroundStyle(Niq.textFaint)
                                .frame(width: 78, alignment: .leading)
                            Text(credential)
                                .font(Niq.mono(10.5))
                                .foregroundStyle(Niq.textFaint)
                                .textSelection(.enabled)
                            Spacer(minLength: 8)
                            Pill(title: "复制") { copyToPasteboard(credential) }
                        }
                    }
                }
            }

            SectionBox(title: "参数配置", subtitle: "构建期输入，重启后生效") {
                VStack(alignment: .leading, spacing: 8) {
                    if box.editingParams {
                        SquareTextEditor(text: $box.paramsText, minHeight: 220, monospaced: true)
                        HStack(spacing: 8) {
                            Spacer()
                            Pill(title: "取消") { box.editingParams = false }
                            Pill(title: "保存", accent: true) { saveParams() }
                        }
                    } else {
                        if let params = worker?.params, !params.isEmpty {
                            CodeBlock(language: "json", code: JSONValue.object(params).jsonText(pretty: true))
                        } else {
                            Text("（无参数）").font(.system(size: Niq.caption)).foregroundStyle(Niq.textFaint)
                        }
                        HStack {
                            Spacer()
                            Pill(title: "编辑") {
                                box.paramsText = JSONValue.object(worker?.params ?? [:]).jsonText(pretty: true)
                                box.editingParams = true
                            }
                        }
                    }
                }
            }
        }
    }

    private func beginMeta() {
        box.label = worker?.label ?? ""
        box.tags = (worker?.tags ?? []).joined(separator: ", ")
        box.description = worker?.description ?? ""
        box.editingMeta = true
    }

    private func saveMeta() {
        let tags = box.tags.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        Task {
            let ok = await model.updateWorkerMeta(workerID, label: box.label, tags: tags, description: box.description)
            box.note = ok ? "已保存备注" : "保存失败"
            if ok { box.editingMeta = false }
        }
    }

    private func saveParams() {
        let (document, error) = parseJSON(box.paramsText)
        guard let document else { box.note = error ?? "JSON 无效"; return }
        Task {
            let ok = await model.updateWorkerParams(workerID, params: document)
            box.note = ok ? "已保存参数（重启后生效）" : "保存失败"
            if ok { box.editingParams = false }
        }
    }

    // MARK: 自述

    private var readmeTab: some View {
        SectionBox(title: "自述（worker.readme）") {
            VStack(alignment: .leading, spacing: 10) {
                if box.readmeLoading {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.mini)
                        Text("读取中…").font(.system(size: Niq.caption)).foregroundStyle(Niq.textFaint)
                    }
                } else if !box.readmeError.isEmpty {
                    Text(box.readmeError)
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.failed)
                } else if box.readme.isEmpty {
                    Text("还没有自述。")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                } else {
                    MarkdownText(raw: box.readme, size: Niq.body)
                }
                HStack {
                    Spacer()
                    Pill(title: "刷新") { loadReadme() }
                }
            }
        }
    }

    private func loadReadme() {
        box.readmeLoading = true
        box.readmeError = ""
        Task {
            let reply = await model.askWorker(workerID, "worker.readme")
            box.readmeLoading = false
            box.readmeLoaded = true
            if reply.ok {
                box.readme = reply.text
            } else {
                box.readmeError = reply.error ?? "该 worker 不提供 worker.readme"
            }
        }
    }

    // MARK: 上下文

    private var contextTab: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let providers, !(providers.providers ?? []).isEmpty {
                SectionBox(title: "模型供应商") {
                    VStack(alignment: .leading, spacing: 10) {
                        HStack(spacing: 8) {
                            Text("当前")
                                .font(.system(size: Niq.caption))
                                .foregroundStyle(Niq.textFaint)
                            Text(providers.current?.provider ?? "—")
                                .font(Niq.mono(Niq.caption))
                                .foregroundStyle(Niq.accent)
                            Text(providers.current?.model ?? "")
                                .font(Niq.mono(Niq.caption))
                                .foregroundStyle(Niq.textDim)
                            Spacer(minLength: 0)
                        }
                        HStack(spacing: 8) {
                            Menu {
                                ForEach(providers.providers ?? []) { option in
                                    Button(option.name) {
                                        box.providerName = option.name
                                        box.modelName = option.defaultModel ?? (option.models?.first ?? "")
                                    }
                                }
                            } label: {
                                pickerLabel(box.providerName.isEmpty ? (providers.current?.provider ?? "选择供应商") : box.providerName)
                            }
                            .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize()

                            Menu {
                                let name = box.providerName.isEmpty ? (providers.current?.provider ?? "") : box.providerName
                                let options = (providers.providers ?? []).first { $0.name == name }
                                ForEach(options?.models ?? [], id: \.self) { model in
                                    Button(model) { box.modelName = model }
                                }
                            } label: {
                                pickerLabel(box.modelName.isEmpty ? (providers.current?.model ?? "选择模型") : box.modelName)
                            }
                            .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize()

                            Pill(title: "切换", accent: true, enabled: !box.busy) {
                                let provider = box.providerName.isEmpty ? (providers.current?.provider ?? "") : box.providerName
                                let chosen = box.modelName.isEmpty ? (providers.current?.model ?? "") : box.modelName
                                guard !provider.isEmpty, !chosen.isEmpty else { return }
                                Task {
                                    box.busy = true
                                    let ok = await model.switchProvider(workerID, provider: provider, model: chosen)
                                    box.busy = false
                                    box.note = ok ? "已切换模型" : "切换失败"
                                }
                            }
                        }
                    }
                }
            } else {
                SectionBox(title: "模型供应商") {
                    Text("该工作单元未上报可切换的供应商（仅 reason / coding 支持）。")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                }
            }

            if let status = model.workerContexts[workerID] {
                SectionBox(title: "上下文状态") {
                    VStack(alignment: .leading, spacing: 6) {
                        field("上下文窗口", status.contextWindow.map { UsageInfo.compact($0) } ?? "—")
                        field("转录 tokens", status.transcriptTokens.map { UsageInfo.compact($0) } ?? "—")
                        field("消息", status.messages.map(String.init) ?? "—")
                        field("轮次", status.rounds.map(String.init) ?? "—")
                        field("输入 / 输出",
                              "\(status.inputTokens.map { UsageInfo.compact($0) } ?? "—") / \(status.outputTokens.map { UsageInfo.compact($0) } ?? "—")")
                        field("模型", status.model ?? "—")
                        if !status.usage.breakdown.isEmpty {
                            UsagePanel(usage: status.usage)
                                .frame(width: 380, alignment: .leading)
                                .padding(.top, 6)
                        }
                    }
                }
            }

            if worker?.isWorkspace == true {
                mountsBox
            }
        }
    }

    private var mountsBox: some View {
        SectionBox(title: "挂载目录") {
            VStack(alignment: .leading, spacing: 8) {
                let mounts = model.workerMounts[workerID]?.mounts ?? []
                if mounts.isEmpty {
                    Text("（无挂载）").font(.system(size: Niq.caption)).foregroundStyle(Niq.textFaint)
                }
                ForEach(mounts, id: \.self) { path in
                    HStack(spacing: 8) {
                        Text(path)
                            .font(Niq.mono(Niq.caption))
                            .foregroundStyle(Niq.textDim)
                            .textSelection(.enabled)
                        if model.workerMounts[workerID]?.primary == path {
                            Chip(text: "主挂载", color: Niq.accent)
                        }
                        Spacer(minLength: 8)
                        Pill(title: "卸载") {
                            Task { box.mountNote = await model.mutateMount(workerID, add: false, path: path) ?? "" }
                        }
                    }
                }
                HStack(spacing: 8) {
                    SquareField(text: $box.newMount, placeholder: "/绝对路径", monospaced: true)
                    Pill(title: "挂载", accent: true) {
                        let path = box.newMount.trimmingCharacters(in: .whitespaces)
                        guard !path.isEmpty else { return }
                        Task {
                            box.mountNote = await model.mutateMount(workerID, add: true, path: path) ?? ""
                            if box.mountNote.isEmpty { box.newMount = "" }
                        }
                    }
                }
                if !box.mountNote.isEmpty {
                    Text(box.mountNote).font(.system(size: Niq.caption)).foregroundStyle(Niq.failed)
                }
            }
        }
    }

    // MARK: 事件

    private var workerEvents: [Event] {
        Array(model.events.filter { $0.involvedWorkers.contains(workerID) }.suffix(150).reversed())
    }

    private var eventsTab: some View {
        SectionBox(title: "最近事件", subtitle: "\(workerEvents.count)") {
            VStack(alignment: .leading, spacing: 0) {
                if workerEvents.isEmpty {
                    Text("还没有该工作单元的事件")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                        .padding(.vertical, 6)
                }
                ForEach(workerEvents) { event in
                    HStack(spacing: 10) {
                        Text(event.timeText)
                            .font(Niq.mono(Niq.micro))
                            .foregroundStyle(Niq.textFaint)
                            .frame(width: 66, alignment: .leading)
                        Text(event.type)
                            .font(Niq.mono(Niq.caption))
                            .foregroundStyle(Niq.textDim)
                            .frame(width: 170, alignment: .leading)
                        Text(Transcript.oneLine(event.summary, 120))
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                            .lineLimit(1)
                        Spacer(minLength: 0)
                    }
                    .padding(.vertical, 5)
                    .contentShape(Rectangle())
                    .onTapGesture {
                        if let trace = event.traceID, !trace.isEmpty {
                            model.openTrace(trace)
                            dismiss()
                        }
                    }
                    Rectangle().fill(Niq.borderSoft).frame(height: 0.5)
                }
            }
        }
    }

    // MARK: 许可

    private var permsTab: some View {
        VStack(alignment: .leading, spacing: 14) {
            SectionBox(title: "订阅许可（subscribe_allow）", subtitle: "事件类型，可选来源 worker") {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(box.subscribe.enumerated()), id: \.offset) { index, pattern in
                        HStack(spacing: 8) {
                            SquareField(text: Binding(
                                get: { box.subscribe[index].type },
                                set: { box.subscribe[index].type = $0 }
                            ), placeholder: "type 或 *", monospaced: true)
                                .frame(width: 220)
                            SquareField(text: Binding(
                                get: { box.subscribe[index].source ?? "" },
                                set: { box.subscribe[index].source = $0.isEmpty ? nil : $0 }
                            ), placeholder: "来源（可选）", monospaced: true)
                                .frame(width: 160)
                            Spacer(minLength: 8)
                            Pill(title: "移除") { box.subscribe.remove(at: index) }
                        }
                    }
                    HStack(spacing: 8) {
                        Pill(title: "添加订阅") { box.subscribe.append(EventPattern(type: "*", source: nil)) }
                        Spacer()
                    }
                }
            }

            SectionBox(title: "发布许可（publish_allow）", subtitle: "事件类型，可选目标 worker") {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(box.publish.enumerated()), id: \.offset) { index, pattern in
                        HStack(spacing: 8) {
                            SquareField(text: Binding(
                                get: { box.publish[index].type },
                                set: { box.publish[index].type = $0 }
                            ), placeholder: "type", monospaced: true)
                                .frame(width: 220)
                            SquareField(text: Binding(
                                get: { box.publish[index].target ?? "" },
                                set: { box.publish[index].target = $0.isEmpty ? nil : $0 }
                            ), placeholder: "目标（可选）", monospaced: true)
                                .frame(width: 160)
                            Spacer(minLength: 8)
                            Pill(title: "移除") { box.publish.remove(at: index) }
                        }
                    }
                    HStack(spacing: 8) {
                        Pill(title: "添加发布") { box.publish.append(PublishPattern(type: "", target: nil)) }
                        Spacer()
                        Pill(title: "保存", accent: true) { savePerms() }
                    }
                }
            }
        }
    }

    private func savePerms() {
        let subscribe = box.subscribe.filter { !$0.type.trimmingCharacters(in: .whitespaces).isEmpty }
        let publish = box.publish.filter { !$0.type.trimmingCharacters(in: .whitespaces).isEmpty }
        Task {
            let ok = await model.updateWorkerAllow(workerID, publish: publish, subscribe: subscribe)
            box.note = ok ? "已保存许可" : "保存失败"
        }
    }

    // MARK: Helpers

    private func loadDrafts() {
        guard !box.permsLoaded else { return }
        box.permsLoaded = true
        box.publish = worker?.publishAllow ?? []
        box.subscribe = worker?.subscribeAllow ?? []
    }

    private func field(_ label: String, _ value: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Text(label)
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 78, alignment: .leading)
            Text(value.isEmpty ? "—" : value)
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textDim)
                .textSelection(.enabled)
            Spacer(minLength: 0)
        }
    }

    private func pickerLabel(_ text: String) -> some View {
        Text(text)
            .font(Niq.mono(Niq.caption))
            .foregroundStyle(Niq.textDim)
            .lineLimit(1)
            .padding(.horizontal, 9)
            .padding(.vertical, 5)
            .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))
    }
}
