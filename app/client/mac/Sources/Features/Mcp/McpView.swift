import SwiftUI

/// MCP workers: ordinary workers that proxy remote MCP servers onto the bus.
///
/// WebUI parity — a rail of MCP workers, and a panel that reads the live server
/// list (`mcp.server.list`), lists a server's tools (`mcp.tool.list`), switches
/// the exposure mode (`mcp.expose`), and adds/removes servers — all through the
/// worker's own events, except for a `fixed` declaration (configured-only).
struct McpView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var selected: String?
        @Published var servers: [MCPServer] = []
        @Published var tools: [String: [MCPTool]] = [:]
        @Published var openServer: String?
        @Published var expose = ""
        @Published var error = ""
        @Published var loading = false
        @Published var note = ""

        // Add-server form
        @Published var draftName = ""
        @Published var draftURL = ""
        @Published var draftProtocol = "streamable-http"
        @Published var draftHeaders = ""
    }

    private var mcpWorkers: [Worker] { model.workers.filter { $0.isMCP } }
    private var worker: Worker? { mcpWorkers.first { $0.id == state.selected } }

    /// The worker's build-time declaration (`params`), as mcp.ParseConfig reads it.
    private var declaration: MCPDeclaration { MCPDeclaration(params: worker?.params) }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "MCP 工作单元", count: mcpWorkers.count) {
                Pill(title: "刷新") {
                    Task { await loadServers() ; await model.reloadWorkers() }
                }
            }

            if mcpWorkers.isEmpty {
                emptyState
            } else {
                HSplitView {
                    rail.frame(minWidth: 220, idealWidth: 260, maxWidth: 340)
                    panel.frame(minWidth: 460)
                }
            }
        }
        .background(Niq.window)
        .task {
            if state.selected == nil { state.selected = mcpWorkers.first?.id }
            await loadServers()
        }
        .onChange(of: state.selected) { _, _ in
            state.servers = []
            state.tools = [:]
            state.openServer = nil
            state.error = ""
            Task { await loadServers() }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "puzzlepiece.extension")
                .font(.system(size: 26, weight: .light))
                .foregroundStyle(Niq.textFaint)
            Text("还没有 MCP 工作单元")
                .font(.system(size: Niq.body))
                .foregroundStyle(Niq.textDim)
            Text("MCP 工作单元把远程 MCP 服务器的工具代理到总线上；一个工作单元可接入多个 server。")
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(maxWidth: 460)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: Rail

    private var rail: some View {
        ScrollView {
            LazyVStack(spacing: 0) {
                ForEach(mcpWorkers) { item in
                    let selected = state.selected == item.id
                    let decl = MCPDeclaration(params: item.params)
                    VStack(alignment: .leading, spacing: 5) {
                        HStack(spacing: 6) {
                            Dot(color: item.online == true ? Niq.completed : Niq.neutral, size: 6)
                            Text(item.displayName)
                                .font(Niq.mono(Niq.caption))
                                .foregroundStyle(selected ? Niq.accent : Niq.text)
                                .lineLimit(1)
                            Spacer(minLength: 4)
                            if item.id != item.displayName {
                                Text(item.id)
                                    .font(Niq.mono(10.5))
                                    .foregroundStyle(Niq.textFaint)
                                    .lineLimit(1)
                            }
                        }
                        HStack(spacing: 6) {
                            Chip(text: decl.fixed ? "固定" : "动态", color: Niq.textDim)
                            if !decl.servers.isEmpty {
                                Chip(text: "\(decl.servers.count) 个 server", color: Niq.textFaint)
                            }
                        }
                    }
                    .padding(.horizontal, 14)
                    .padding(.vertical, 9)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
                    .background(selected ? Niq.accentSoft.opacity(0.6) : Color.clear)
                    .onTapGesture { state.selected = item.id }
                }
            }
        }
    }

    // MARK: Panel

    @ViewBuilder
    private var panel: some View {
        if let worker {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    header(worker)
                    if !state.error.isEmpty {
                        Text(state.error)
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.failed)
                    }
                    declarationBox(worker)
                    serversBox(worker)
                    if !declaration.fixed {
                        addServerBox(worker)
                    }
                }
                .padding(Niq.pagePadding)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            Text("选择一个 MCP 工作单元")
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func header(_ worker: Worker) -> some View {
        HStack(spacing: 8) {
            Text(worker.id)
                .font(Niq.mono(Niq.body, weight: .semibold))
            Chip(text: worker.type ?? "mcp", color: Niq.textDim)
            if let description = worker.description, !description.isEmpty {
                Text(description)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textFaint)
                    .lineLimit(1)
            }
            Spacer(minLength: 12)
            if state.loading { ProgressView().controlSize(.mini) }
            if !state.note.isEmpty {
                Text(state.note)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textFaint)
            }
            Pill(title: "查看详情") { model.workerDetail = WorkerRef(id: worker.id) }
        }
    }

    private func declarationBox(_ worker: Worker) -> some View {
        SectionBox(title: "声明", subtitle: declaration.fixed ? "纯配置型：server 与暴露方式以声明为准" : "可在运行时修改") {
            VStack(alignment: .leading, spacing: 8) {
                FormRow(label: "模式") {
                    Text(declaration.fixed ? "固定" : "动态")
                        .font(.system(size: 12))
                        .foregroundStyle(Niq.textDim)
                }
                FormRow(label: "暴露方式") {
                    if declaration.fixed {
                        Text(declaration.expose)
                            .font(Niq.mono(12))
                            .foregroundStyle(Niq.textDim)
                    } else {
                        Menu {
                            Button("agentic") { setExpose(worker, "agentic") }
                            Button("ready") { setExpose(worker, "ready") }
                        } label: {
                            Text(state.expose.isEmpty ? declaration.expose : state.expose)
                                .font(Niq.mono(12))
                                .foregroundStyle(Niq.textDim)
                        }
                        .menuStyle(.borderlessButton)
                        .menuIndicator(.hidden)
                        .fixedSize()
                        Text(declaration.expose == "ready" ? "每个远程工具注册为独立能力" : "按需发现：list → tool.list → tool.call")
                            .font(.system(size: Niq.micro))
                            .foregroundStyle(Niq.textFaint)
                    }
                }
                if !declaration.allowedTools.isEmpty {
                    FormRow(label: "允许的工具") {
                        Text(declaration.allowedTools.joined(separator: ", "))
                            .font(Niq.mono(11.5))
                            .foregroundStyle(Niq.textDim)
                            .lineLimit(2)
                    }
                }
            }
        }
    }

    private func serversBox(_ worker: Worker) -> some View {
        SectionBox(title: "服务器", subtitle: "\(state.servers.count)") {
            VStack(alignment: .leading, spacing: 0) {
                if state.servers.isEmpty {
                    Text(state.loading ? "读取中…" : "没有服务器")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                        .padding(.vertical, 6)
                }
                ForEach(state.servers) { server in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(spacing: 8) {
                            Dot(color: server.status == "connected" ? Niq.completed : Niq.neutral, size: 6)
                            Text(server.name)
                                .font(Niq.mono(Niq.caption, weight: .semibold))
                            Text(server.url)
                                .font(Niq.mono(11))
                                .foregroundStyle(Niq.textFaint)
                                .lineLimit(1)
                            Spacer(minLength: 8)
                            if let tools = server.tools {
                                Text("\(tools) 个工具")
                                    .font(.system(size: Niq.micro))
                                    .foregroundStyle(Niq.textFaint)
                            }
                            Pill(title: state.openServer == server.name ? "收起" : "工具") {
                                toggleTools(worker, server)
                            }
                            if !declaration.fixed {
                                Pill(title: "移除") { removeServer(worker, server.name) }
                            }
                        }
                        if state.openServer == server.name {
                            VStack(alignment: .leading, spacing: 4) {
                                if let tools = state.tools[server.name], !tools.isEmpty {
                                    ForEach(tools) { tool in
                                        HStack(alignment: .top, spacing: 8) {
                                            Text(tool.name)
                                                .font(Niq.mono(11.5))
                                                .foregroundStyle(Niq.accent)
                                                .frame(width: 200, alignment: .leading)
                                            Text(tool.description ?? "")
                                                .font(.system(size: 11))
                                                .foregroundStyle(Niq.textFaint)
                                                .lineLimit(2)
                                            Spacer(minLength: 0)
                                        }
                                    }
                                } else {
                                    Text("读取工具中…")
                                        .font(.system(size: 11))
                                        .foregroundStyle(Niq.textFaint)
                                }
                            }
                            .padding(.leading, 14)
                            .padding(.top, 4)
                        }
                    }
                    .padding(.vertical, 7)
                    Rectangle().fill(Niq.borderSoft).frame(height: 0.5)
                }
            }
        }
    }

    private func addServerBox(_ worker: Worker) -> some View {
        SectionBox(title: "添加服务器", subtitle: "远程 Streamable HTTP") {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    SquareField(text: $state.draftName, placeholder: "名称", monospaced: true)
                        .frame(width: 140)
                    SquareField(text: $state.draftURL, placeholder: "https://…", monospaced: true)
                    Menu {
                        ForEach(["streamable-http", "sse"], id: \.self) { protocolName in
                            Button(protocolName) { state.draftProtocol = protocolName }
                        }
                    } label: {
                        Text(state.draftProtocol)
                            .font(Niq.mono(Niq.caption))
                            .foregroundStyle(Niq.textDim)
                    }
                    .menuStyle(.borderlessButton)
                    .menuIndicator(.hidden)
                    .fixedSize()
                    Pill(title: "添加", accent: true) { addServer(worker) }
                }
                SquareTextEditor(text: $state.draftHeaders, minHeight: 64, monospaced: true,
                                 placeholder: "可选请求头，每行 KEY=VALUE")
            }
        }
    }

    // MARK: Actions

    private func loadServers() async {
        guard let worker else { return }
        state.loading = true
        defer { state.loading = false }
        let reply = await model.askWorker(worker.id, "mcp.server.list")
        if reply.ok {
            state.servers = MCPServer.list(from: reply.result)
            state.error = ""
        } else {
            state.servers = []
            state.error = reply.error ?? "mcp.server.list 失败"
        }
        if !declaration.fixed {
            let expose = await model.askWorker(worker.id, "mcp.expose")
            if expose.ok, let mode = expose.result?.object?["mode"]?.string { state.expose = mode }
        }
    }

    private func toggleTools(_ worker: Worker, _ server: MCPServer) {
        if state.openServer == server.name { state.openServer = nil; return }
        state.openServer = server.name
        guard state.tools[server.name] == nil else { return }
        Task {
            let reply = await model.askWorker(worker.id, "mcp.tool.list", payload: ["server": server.name])
            if reply.ok {
                state.tools[server.name] = MCPTool.list(from: reply.result)
            } else {
                state.error = reply.error ?? "mcp.tool.list 失败"
            }
        }
    }

    private func setExpose(_ worker: Worker, _ mode: String) {
        Task {
            let reply = await model.askWorker(worker.id, "mcp.expose", payload: ["mode": mode])
            state.note = reply.ok ? "暴露方式：\(mode)" : (reply.error ?? "切换失败")
            if reply.ok { state.expose = mode }
        }
    }

    private func addServer(_ worker: Worker) {
        let name = state.draftName.trimmingCharacters(in: .whitespaces)
        let url = state.draftURL.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty, url.hasPrefix("http") else {
            state.error = "需要名称与 http(s) URL"
            return
        }
        var payload: [String: Any] = ["name": name, "url": url, "protocol": state.draftProtocol]
        let headers = parseHeaderLines(state.draftHeaders)
        if !headers.isEmpty { payload["headers"] = headers }
        Task {
            let reply = await model.askWorker(worker.id, "mcp.server.add", payload: payload, timeout: 35)
            if reply.ok {
                state.draftName = ""; state.draftURL = ""; state.draftHeaders = ""
                state.note = "已添加 \(name)"
                await loadServers()
            } else {
                state.error = reply.error ?? "添加失败"
            }
        }
    }

    private func removeServer(_ worker: Worker, _ name: String) {
        confirmDestructive("移除服务器", "从 \(worker.id) 移除 \(name)？") {
            Task {
                let reply = await model.askWorker(worker.id, "mcp.server.remove",
                                                  payload: ["name": name], timeout: 35)
                if reply.ok {
                    state.note = "已移除 \(name)"
                    await loadServers()
                } else {
                    state.error = reply.error ?? "移除失败"
                }
            }
        }
    }

    private func parseHeaderLines(_ text: String) -> [String: String] {
        var out: [String: String] = [:]
        for line in text.split(separator: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { continue }
            guard let index = trimmed.firstIndex(of: "="), index != trimmed.startIndex else { continue }
            let key = trimmed[..<index].trimmingCharacters(in: .whitespaces)
            let value = trimmed[trimmed.index(after: index)...].trimmingCharacters(in: .whitespaces)
            if !key.isEmpty { out[key] = value }
        }
        return out
    }
}

// MARK: - Wire shapes

struct MCPServer: Identifiable, Hashable {
    let name: String
    let url: String
    let status: String?
    let tools: Int?
    var id: String { name }

    static func list(from value: JSONValue?) -> [MCPServer] {
        (value?.array ?? []).compactMap { entry in
            guard let object = entry.object, let name = object["name"]?.string else { return nil }
            return MCPServer(
                name: name,
                url: object["url"]?.string ?? "",
                status: object["status"]?.string,
                tools: object["tools"]?.number.map { Int($0) }
            )
        }
    }
}

struct MCPTool: Identifiable, Hashable {
    let name: String
    let description: String?
    var id: String { name }

    static func list(from value: JSONValue?) -> [MCPTool] {
        (value?.array ?? []).compactMap { entry in
            guard let object = entry.object, let name = object["name"]?.string else { return nil }
            return MCPTool(name: name, description: object["description"]?.string)
        }
    }
}

/// The build-time MCP declaration (`params`), including the legacy nested
/// `params.mcp` form with flat keys winning.
struct MCPDeclaration {
    var fixed = false
    var expose = "agentic"
    var servers: [String] = []
    var allowedTools: [String] = []

    init(params: [String: JSONValue]?) {
        let flat = params ?? [:]
        let nested = flat["mcp"]?.object ?? [:]
        var merged = nested
        for (key, value) in flat { merged[key] = value }
        fixed = merged["fixed"]?.bool ?? false
        if let mode = merged["expose"]?.string, !mode.isEmpty { expose = mode }
        servers = (merged["servers"]?.array ?? []).compactMap { $0.object?["name"]?.string }
        allowedTools = (merged["allowed_tools"]?.array ?? []).compactMap { $0.string }
    }
}
