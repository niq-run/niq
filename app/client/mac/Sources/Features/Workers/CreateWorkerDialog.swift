import SwiftUI

/// Create a worker: managed (in-process), external (subprocess), or remote.
///
/// Mirrors the WebUI's dialog body for body: `{type, id, placement, label?,
/// tags?, description?, params?, command?, cwd?, token_ttl_seconds?}`.
struct CreateWorkerDialog: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @StateObject private var box = Box()

    private final class Box: ObservableObject {
        @Published var mode = 0                 // 0 managed, 1 external, 2 remote
        @Published var id = ""
        @Published var workerLabel = ""
        @Published var tags = ""
        @Published var description = ""
        @Published var type = "reason"
        @Published var externalType = ""
        @Published var instruction = ""
        @Published var goal = ""
        @Published var brief = ""
        @Published var readme = ""
        @Published var provider = ""
        @Published var modelName = ""
        @Published var rootDir = ""
        @Published var toolMode = "default"
        @Published var mcpExpose = "agentic"
        @Published var mcpFixed = false
        @Published var mcpServers = ""
        @Published var mcpAllowed = ""
        @Published var command = ""
        @Published var cwd = ""
        @Published var envText = ""
        @Published var tokenTTLDays = ""
        @Published var note = ""
        @Published var busy = false
    }

    private let managedTypes = ["reason", "coding", "niq", "workspace", "program", "mcp", "directory", "history", "timer", "host"]

    private var effectiveType: String {
        box.mode == 0 ? box.type : box.externalType.trimmingCharacters(in: .whitespaces)
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Text("创建工作单元")
                    .font(.system(size: Niq.title, weight: .semibold))
                Spacer()
                if !box.note.isEmpty {
                    Text(box.note)
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.failed)
                        .lineLimit(1)
                }
                Pill(title: "取消") { dismiss() }
                Pill(title: "创建", accent: true, enabled: !box.busy) { submit() }
            }
            .padding(Niq.pagePadding)
            .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }

            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    SectionBox(title: "形态") {
                        VStack(alignment: .leading, spacing: 10) {
                            SquareTabs(items: ["托管", "子进程", "三方"], selection: $box.mode)
                            Text(box.mode == 0
                                 ? "托管：由 host 在工作单元内构建并监督。"
                                 : box.mode == 1
                                 ? "子进程：由项目 supervisor 启动并监督一个外部进程。"
                                 : "三方：本机不启动任何进程，由外部自行接入。")
                                .font(.system(size: Niq.micro))
                                .foregroundStyle(Niq.textFaint)
                        }
                    }

                    SectionBox(title: "身份") {
                        VStack(alignment: .leading, spacing: 8) {
                            FormRow(label: "ID") {
                                SquareField(text: $box.id, placeholder: "worker-id", monospaced: true)
                                    .frame(width: 260)
                            }
                            FormRow(label: "类型") {
                                if box.mode == 0 {
                                    Menu {
                                        ForEach(managedTypes, id: \.self) { name in
                                            Button(name) { box.type = name }
                                        }
                                    } label: {
                                        Text(box.type).font(Niq.mono(12)).foregroundStyle(Niq.textDim)
                                    }
                                    .menuStyle(.borderlessButton)
                                    .menuIndicator(.hidden)
                                    .fixedSize()
                                } else {
                                    SquareField(text: $box.externalType, placeholder: "reason", monospaced: true)
                                        .frame(width: 200)
                                }
                            }
                            FormRow(label: "备注名") {
                                SquareField(text: $box.workerLabel, placeholder: "显示名称（可选）").frame(width: 280)
                            }
                            FormRow(label: "标签") {
                                SquareField(text: $box.tags, placeholder: "用逗号分隔，形成层级").frame(width: 320)
                            }
                            FormRow(label: "描述") {
                                SquareField(text: $box.description, placeholder: "一句话说明").frame(width: 380)
                            }
                        }
                    }

                    if box.mode == 0 {
                        managedSection
                    } else if box.mode == 1 {
                        SectionBox(title: "进程") {
                            VStack(alignment: .leading, spacing: 8) {
                                FormRow(label: "命令") {
                                    SquareField(text: $box.command, placeholder: "node worker.js --flag", monospaced: true)
                                }
                                FormRow(label: "工作目录") {
                                    SquareField(text: $box.cwd, placeholder: "可选绝对路径", monospaced: true)
                                }
                                FormRow(label: "环境变量") {
                                    SquareTextEditor(text: $box.envText, minHeight: 80, monospaced: true,
                                                     placeholder: "每行 KEY=VALUE")
                                }
                            }
                        }
                    } else {
                        SectionBox(title: "凭证") {
                            FormRow(label: "有效期（天）") {
                                SquareField(text: $box.tokenTTLDays, placeholder: "留空 = 默认", monospaced: true)
                                    .frame(width: 140)
                            }
                        }
                    }
                }
                .padding(Niq.pagePadding)
            }
        }
        .background(Niq.window)
        .onAppear {
            if box.id.isEmpty { box.id = "" }
        }
    }

    // MARK: Managed params

    @ViewBuilder
    private var managedSection: some View {
        if box.type == "reason" || box.type == "coding" {
            SectionBox(title: "推理配置") {
                VStack(alignment: .leading, spacing: 8) {
                    FormRow(label: "指令") {
                        SquareTextEditor(text: $box.instruction, minHeight: 72, monospaced: false,
                                         placeholder: "系统提示词（可选）")
                    }
                    FormRow(label: "目标") {
                        SquareField(text: $box.goal, placeholder: "goal（可选）")
                    }
                    FormRow(label: "开场消息") {
                        SquareField(text: $box.brief, placeholder: "brief（可选）")
                    }
                    FormRow(label: "自述") {
                        SquareTextEditor(text: $box.readme, minHeight: 64, monospaced: false,
                                         placeholder: "worker.readme，可被其他 worker 读取")
                    }
                    FormRow(label: "供应商") {
                        SquareField(text: $box.provider, placeholder: "留空 = 默认", monospaced: true).frame(width: 200)
                    }
                    FormRow(label: "模型") {
                        SquareField(text: $box.modelName, placeholder: "留空 = 默认", monospaced: true).frame(width: 240)
                    }
                    if box.type == "coding" {
                        FormRow(label: "工具模式") {
                            SquareField(text: $box.toolMode, placeholder: "default", monospaced: true).frame(width: 160)
                        }
                    }
                }
            }
        } else if box.type == "workspace" || box.type == "program" {
            SectionBox(title: "挂载") {
                FormRow(label: "根目录") {
                    SquareField(text: $box.rootDir, placeholder: "/Users/…", monospaced: true)
                }
            }
        } else if box.type == "mcp" {
            SectionBox(title: "MCP 配置") {
                VStack(alignment: .leading, spacing: 8) {
                    FormRow(label: "暴露方式") {
                        Menu {
                            Button("agentic") { box.mcpExpose = "agentic" }
                            Button("ready") { box.mcpExpose = "ready" }
                        } label: {
                            Text(box.mcpExpose).font(Niq.mono(12)).foregroundStyle(Niq.textDim)
                        }
                        .menuStyle(.borderlessButton)
                        .menuIndicator(.hidden)
                        .fixedSize()
                    }
                    FormRow(label: "固定") {
                        Toggle("", isOn: $box.mcpFixed).labelsHidden().toggleStyle(.switch).controlSize(.small)
                    }
                    FormRow(label: "允许的工具") {
                        SquareField(text: $box.mcpAllowed, placeholder: "逗号分隔，留空 = 全部").frame(width: 320)
                    }
                    FormRow(label: "Servers") {
                        SquareTextEditor(text: $box.mcpServers, minHeight: 110, monospaced: true,
                                         placeholder: "[{\"name\":\"x\",\"url\":\"https://…\"}]")
                    }
                }
            }
        } else if box.type == "niq" {
            SectionBox(title: "niq 桥接") {
                Text("niq 工作单元需要远端总线 URL 与 worker id（写入 params.remote_url / remote_worker_id）。请在创建后用参数编辑补充。")
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textFaint)
            }
        }
    }

    // MARK: Submit

    private func submit() {
        let id = box.id.trimmingCharacters(in: .whitespaces)
        guard !id.isEmpty else { box.note = "需要 ID"; return }
        let type = effectiveType
        guard !type.isEmpty else { box.note = "需要类型"; return }

        let placement = box.mode == 0 ? "in-process" : (box.mode == 2 ? "unmanaged-process" : "managed-process")
        var body: [String: Any] = ["type": type, "id": id, "placement": placement]
        if !box.workerLabel.trimmingCharacters(in: .whitespaces).isEmpty { body["label"] = box.workerLabel }
        let tags = box.tags.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        if !tags.isEmpty { body["tags"] = tags }
        if !box.description.trimmingCharacters(in: .whitespaces).isEmpty { body["description"] = box.description }

        var params: [String: Any] = [:]
        if box.mode == 0 {
            if box.type == "reason" || box.type == "coding" {
                if !box.instruction.isEmpty { params["instruction"] = box.instruction }
                if !box.provider.isEmpty { params["provider"] = box.provider }
                if !box.modelName.isEmpty { params["model"] = box.modelName }
                if !box.goal.isEmpty { params["goal"] = box.goal }
                if !box.brief.isEmpty { params["brief"] = box.brief }
                if !box.readme.isEmpty { params["readme"] = box.readme }
                if box.type == "coding" { params["tool_mode"] = box.toolMode }
            }
            if (box.type == "workspace" || box.type == "program"),
               !box.rootDir.trimmingCharacters(in: .whitespaces).isEmpty {
                params["mounts"] = [box.rootDir]
            }
            if box.type == "mcp" {
                if !box.mcpServers.trimmingCharacters(in: .whitespaces).isEmpty {
                    guard let data = box.mcpServers.data(using: .utf8),
                          let servers = try? JSONSerialization.jsonObject(with: data) as? [Any] else {
                        box.note = "servers 需要 JSON 数组"
                        return
                    }
                    params["servers"] = servers
                } else {
                    params["servers"] = []
                }
                params["expose"] = box.mcpExpose
                params["fixed"] = box.mcpFixed
                let allowed = box.mcpAllowed.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
                if !allowed.isEmpty { params["allowed_tools"] = allowed }
            }
        } else if box.mode == 1 {
            let command = box.command.split(separator: " ").map(String.init)
            if !command.isEmpty { body["command"] = command }
            if !box.cwd.isEmpty { body["cwd"] = box.cwd }
            for line in box.envText.split(separator: "\n") {
                let trimmed = line.trimmingCharacters(in: .whitespaces)
                guard let index = trimmed.firstIndex(of: "="), index != trimmed.startIndex else { continue }
                let key = trimmed[..<index].trimmingCharacters(in: .whitespaces)
                let value = trimmed[trimmed.index(after: index)...].trimmingCharacters(in: .whitespaces)
                if !key.isEmpty { params[key] = value }
            }
        } else {
            if let days = Double(box.tokenTTLDays.trimmingCharacters(in: .whitespaces)), days > 0 {
                body["token_ttl_seconds"] = Int(days * 86400)
            }
        }
        if !params.isEmpty { body["params"] = params }

        box.busy = true
        Task {
            let ok = await model.createWorker(body)
            box.busy = false
            if ok { dismiss() } else { box.note = "创建失败（见底部提示）" }
        }
    }
}
