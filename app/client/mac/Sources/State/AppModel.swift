import Foundation
import Combine

/// How a message is delivered to the target worker (mirrors the WebUI's modes).
enum TalkInputMode: String, CaseIterable, Identifiable {
    case append, schedule, interrupt
    var id: String { rawValue }
    var wire: String { rawValue }
    var label: String {
        switch self {
        case .append: return "追加模式"
        case .schedule: return "预约模式"
        case .interrupt: return "打断模式"
        }
    }

    var hint: String {
        switch self {
        case .schedule: return "空闲时才处理（最不打扰）"
        case .append: return "不打断当前推理，下一轮尽快响应"
        case .interrupt: return "取消进行中的推理，立即处理"
        }
    }
}

/// The appearance override the WebUI exposes as 深色 in its footer.
enum AppearanceMode: String, CaseIterable, Identifiable {
    case system, light, dark
    var id: String { rawValue }
    var label: String {
        switch self {
        case .system: return "跟随系统"
        case .light: return "浅色"
        case .dark: return "深色"
        }
    }
    var shortLabel: String {
        switch self {
        case .system: return "外观"
        case .light: return "浅色"
        case .dark: return "深色"
        }
    }
}

@MainActor
final class AppModel: ObservableObject {
    enum Section: String, CaseIterable, Identifiable {
        case talk, events, approvals, workers, mcp, programs, projects, templates, models
        var id: String { rawValue }
        static let primary: [Section] = [.talk, .events, .approvals]
        static let projectResources: [Section] = [.workers, .mcp, .programs]
        static let resources: [Section] = [.projects, .templates, .models]
        var title: String {
            switch self {
            case .talk: return "对话"
            case .events: return "事件"
            case .approvals: return "审批"
            case .workers: return "工作单元"
            case .mcp: return "MCP 工作单元"
            case .programs: return "程序"
            case .projects: return "项目"
            case .templates: return "模板"
            case .models: return "模型供应商"
            }
        }
        var icon: String {
            switch self {
            case .talk: return "bubble.left.and.bubble.right"
            case .events: return "list.bullet.rectangle"
            case .approvals: return "checkmark.shield"
            case .workers: return "square.stack.3d.up"
            case .mcp: return "puzzlepiece.extension"
            case .programs: return "text.book.closed"
            case .projects: return "square.grid.2x2"
            case .templates: return "square.on.square"
            case .models: return "cpu"
            }
        }
    }

    @Published var gatewayURL = URL(string: "http://127.0.0.1:9527")!
    @Published var gatewayRunning = false
    @Published var projects: [ProjectView] = []
    @Published var selectedProjectID: String?
    @Published var section: Section = .talk
    @Published var workers: [Worker] = []
    @Published var workerNames: [String: String] = [:]
    @Published var events: [Event] = []

    /// Sidebar checkboxes: which workers' events the talk view shows (empty = all).
    @Published var viewFilter: Set<String> = []
    /// Composer: where a message is sent.
    @Published var sendTargets: Set<String> = []
    @Published var inputMode: TalkInputMode = .append
    @Published var appearance: AppearanceMode = .system

    // Talk view settings (mirror the WebUI's toggles).
    @Published var streamingMode = false
    @Published var expandThinking = false
    @Published var collapseProcess = true
    @Published var responseOnly = false

    @Published var streamingText = ""
    @Published var streamingThinking = ""
    @Published var providers: ProviderConfig?
    @Published var eventsFilterTrace: String?
    @Published var workerDetail: WorkerRef?
    @Published var workerProviders: [String: WorkerProviders] = [:]
    @Published var approvals: [ApprovalEntry] = []
    @Published var archived: Set<String> = []
    @Published var programs: [ProgramInfo] = []
    @Published var templates: [String] = []
    @Published var providersDoc: JSONValue?
    @Published var workerContexts: [String: WorkerContextStatus] = [:]
    @Published var workerMounts: [String: MountList] = [:]
    @Published var watchByWorker: [String: [WatchEntry]] = [:]
    @Published var loadingEarlier = false
    @Published var hasEarlier = true
    @Published var errorMessage: String?
    @Published var busy = false
    @Published var loading = false

    private var streamTask: Task<Void, Never>?
    private var seededHistory = false

    var client: GatewayClient { GatewayClient(base: gatewayURL) }
    var selectedProject: ProjectView? { projects.first { $0.id == selectedProjectID } }
    var talkPartners: [Worker] { workers.filter { $0.isTalkPartner } }
    func worker(_ id: String) -> Worker? { workers.first { $0.id == id } }
    func workerName(_ id: String) -> String { workerNames[id] ?? id }
    var pendingApprovals: [ApprovalEntry] { approvals.filter { $0.isPending } }

    func boot() async {
        if let raw = ProcessInfo.processInfo.environment["NIQ_GATEWAY_URL"], let url = URL(string: raw) {
            gatewayURL = url
        }
        await refreshStatus()
        await loadProjects()
        if selectedProjectID == nil {
            selectedProjectID = projects.first(where: { $0.running == true })?.id ?? projects.first?.id
        }
        await loadProviders()
        await connectSelected()
    }

    func refreshStatus() async {
        if let snap = NiqCLI.status(), let raw = snap.gateway.url, let url = URL(string: raw) {
            gatewayURL = url
            gatewayRunning = snap.gateway.running ?? true
            return
        }
        if let url = GatewayDiscovery.shellStateURL() {
            gatewayURL = url
            gatewayRunning = true
            return
        }
        gatewayRunning = false
    }

    func loadProjects() async {
        loading = true
        defer { loading = false }
        do {
            projects = try await client.listProjects()
            errorMessage = nil
        } catch {
            errorMessage = "项目列表：\(error.localizedDescription)"
        }
    }

    func loadProviders() async {
        do { providers = try await client.providers() } catch { log("providers error:", error.localizedDescription) }
    }

    func selectProject(_ id: String?) {
        selectedProjectID = id
        Task { await connectSelected() }
    }

    func connectSelected() async {
        streamTask?.cancel()
        streamTask = nil
        seededHistory = false
        events = []
        workers = []
        workerNames = [:]
        viewFilter = []
        sendTargets = []
        workerProviders = [:]
        approvals = []
        hasEarlier = true
        streamingText = ""
        streamingThinking = ""
        guard let id = selectedProjectID else { return }

        do {
            let list = try await client.workers(id)
            workers = list
            workerNames = Dictionary(list.map { ($0.id, $0.displayName) }, uniquingKeysWith: { a, _ in a })
            if let preferred = list.first(where: { $0.isTalkPartner && ($0.online ?? true) }) ?? list.first {
                sendTargets = [preferred.id]
            }
        } catch {
            errorMessage = "worker 列表：\(error.localizedDescription)"
            log("workers error:", error.localizedDescription)
            return
        }

        await loadApprovals()
        await loadArchived()

        streamTask = Task { [weak self] in
            guard let self else { return }
            let stream = self.client.stream(project: id) { watermark in
                Task { @MainActor in await self.seedHistory(project: id, anchor: watermark) }
            }
            do {
                for try await event in stream { self.append(event) }
            } catch is CancellationError {
            } catch {
                if !Task.isCancelled {
                    self.errorMessage = "事件流：\(error.localizedDescription)"
                    self.log("stream error:", error.localizedDescription)
                }
            }
        }
    }

    private func seedHistory(project: String, anchor: String) async {
        guard !seededHistory else { return }
        seededHistory = true
        do { merge(try await client.history(project, before: anchor, limit: 60)) }
        catch { log("history error:", error.localizedDescription) }
    }

    private func merge(_ older: [Event]) {
        var byID = Dictionary(events.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        for event in older { byID[event.id] = event }
        events = byID.values.sorted(by: eventOrder)
    }

    private func append(_ event: Event) {
        if event.transient == true {
            guard streamingMode else { return }
            switch event.type {
            case "reason.text_delta": streamingText += deltaText(event)
            case "reason.thinking_delta": streamingThinking += deltaText(event)
            default: break
            }
            return
        }
        if event.type == "reason.response" || event.type == "reason.end" {
            streamingText = ""
            streamingThinking = ""
        }
        // A worker's announced capabilities ride on worker.ready; the detail
        // panel uses them for the readme tab and the send-event form.
        if event.type == "worker.ready", let workerID = event.workerID,
           let list = event.payload?["watch"]?.array {
            let entries: [WatchEntry] = list.compactMap { item in
                guard let object = item.object, let name = object["event"]?.string else { return nil }
                return WatchEntry(event: name, desc: object["desc"]?.string, parameters: object["parameters"])
            }
            if !entries.isEmpty { watchByWorker[workerID] = entries }
        }
        if events.contains(where: { $0.id == event.id }) { return }
        events.append(event)
        if events.count > 3000 { events.removeFirst(events.count - 3000) }
    }

    private func deltaText(_ event: Event) -> String {
        if let text = event.payload?["text"]?.string { return text }
        if let delta = event.payload?["delta"]?.string { return delta }
        if let content = event.payload?["content"]?.array {
            return content.compactMap { $0.object?["text"]?.string }.joined()
        }
        return ""
    }

    private func eventOrder(_ a: Event, _ b: Event) -> Bool {
        let ta = a.timestamp ?? 0, tb = b.timestamp ?? 0
        return ta == tb ? a.id < b.id : ta < tb
    }

    func toggleViewFilter(_ id: String) {
        if viewFilter.contains(id) { viewFilter.remove(id) } else { viewFilter.insert(id) }
    }
    func toggleSendTarget(_ id: String) {
        if sendTargets.contains(id) { sendTargets.remove(id) } else { sendTargets.insert(id) }
    }

    func openTrace(_ trace: String?) {
        eventsFilterTrace = trace
        section = .events
    }

    // MARK: Approvals

    func loadApprovals() async {
        guard let id = selectedProjectID else { return }
        do { approvals = try await client.approvals(id) }
        catch { log("approvals:", error.localizedDescription) }
    }

    func decide(_ approval: ApprovalEntry, approved: Bool, note: String = "") async {
        guard let project = selectedProjectID else { return }
        do {
            try await client.decideApproval(project, approval.id, approved: approved, note: note)
            await loadApprovals()
        } catch {
            errorMessage = "审批：\(error.localizedDescription)"
        }
    }

    // MARK: History paging

    /// Fetch one page older than the oldest event we hold.
    func loadEarlier() async {
        guard let id = selectedProjectID, !loadingEarlier, hasEarlier, let oldest = events.first?.id else { return }
        loadingEarlier = true
        defer { loadingEarlier = false }
        do {
            let older = try await client.history(id, before: oldest, limit: 100)
            if older.isEmpty { hasEarlier = false } else { merge(older) }
        } catch {
            errorMessage = "更早的事件：\(error.localizedDescription)"
        }
    }

    /// Upload one file into the project's upload dir (used by the composer).
    func uploadAttachment(_ url: URL) async -> UploadedFile? {
        guard let id = selectedProjectID else { return nil }
        do { return try await client.upload(id, fileURL: url) }
        catch {
            errorMessage = "上传：\(error.localizedDescription)"
            return nil
        }
    }

    // MARK: Workers

    func reloadWorkers() async {
        guard let id = selectedProjectID else { return }
        do {
            let list = try await client.workers(id)
            workers = list
            workerNames = Dictionary(list.map { ($0.id, $0.displayName) }, uniquingKeysWith: { a, _ in a })
        } catch {
            errorMessage = "worker 列表：\(error.localizedDescription)"
        }
    }

    func workerAction(_ workerID: String, _ action: String) async {
        guard let project = selectedProjectID else { return }
        busy = true
        defer { busy = false }
        do {
            try await client.workerAction(project, workerID, action)
            await reloadWorkers()
        } catch {
            errorMessage = "\(action) \(workerID)：\(error.localizedDescription)"
        }
    }

    func loadWorkerProviders(_ workerID: String) async {
        guard let project = selectedProjectID else { return }
        do {
            workerProviders[workerID] = try await client.workerProviders(project, workerID)
        } catch {
            // A non-reason worker answers 4xx here; that is the normal case, so
            // it must not surface as an app error.
            log("providers \(workerID):", error.localizedDescription)
        }
    }

    @discardableResult
    func switchProvider(_ workerID: String, provider: String, model: String) async -> Bool {
        guard let project = selectedProjectID else { return false }
        do {
            try await client.setWorkerProvider(project, workerID, provider: provider, model: model)
            await loadWorkerProviders(workerID)
            return true
        } catch {
            errorMessage = "切换模型：\(error.localizedDescription)"
            return false
        }
    }

    func send(text: String, attachments: [StagedAttachment] = []) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (!trimmed.isEmpty || !attachments.isEmpty), let id = selectedProjectID else { return }
        let targets = Array(sendTargets)
        guard !targets.isEmpty else { errorMessage = "请选择发送对象"; return }

        var blocks: [Block] = []
        if !trimmed.isEmpty {
            blocks.append(Block(type: "text", text: trimmed, name: nil, mime: nil, data: nil, path: nil, size: nil))
        }
        for attachment in attachments {
            switch attachment.kind {
            case .image:
                blocks.append(Block(type: "image", text: nil, name: attachment.name, mime: attachment.mime,
                                    data: attachment.data, path: nil, size: attachment.size))
            case .file:
                blocks.append(Block(type: "file", text: nil, name: attachment.name, mime: attachment.mime,
                                    data: nil, path: attachment.path, size: attachment.size))
            }
        }

        let body = InputBody(
            content: blocks,
            targets: targets,
            target: targets.first,
            input_mode: inputMode.wire
        )
        do {
            try await client.sendInput(id, body: body)
            errorMessage = nil
        } catch {
            errorMessage = "发送：\(error.localizedDescription)"
            log("send error:", error.localizedDescription)
        }
    }

    func abort() async {
        guard let id = selectedProjectID else { return }
        for target in sendTargets { try? await client.abort(id, target: target) }
    }

    func projectAction(_ action: String, _ id: String) async {
        busy = true
        defer { busy = false }
        do {
            switch action {
            case "start": try await client.start(id)
            case "stop": try await client.stop(id)
            case "restart": try await client.restart(id)
            default: break
            }
            await loadProjects()
            if id == selectedProjectID { await connectSelected() }
        } catch {
            errorMessage = "\(action)：\(error.localizedDescription)"
        }
    }

    func startGateway() async { NiqCLI.startGateway(); await refreshStatus(); await loadProjects() }
    func stopGateway() async { NiqCLI.stopGateway(); await refreshStatus() }

    // MARK: - Extended surfaces

    func isArchived(_ workerID: String) -> Bool { archived.contains(workerID) }

    func askWorker(_ workerID: String, _ type: String,
                   payload: [String: Any] = [:], timeout: TimeInterval = 20) async -> AskReply {
        guard let project = selectedProjectID else {
            return AskReply(ok: false, result: nil, error: "未选择项目")
        }
        do { return try await client.ask(project, workerID, type: type, payload: payload, timeout: timeout) }
        catch { return AskReply(ok: false, result: nil, error: error.localizedDescription) }
    }

    func loadArchived() async {
        guard let project = selectedProjectID else { return }
        archived = Set((try? await client.archived(project)) ?? [])
    }

    func setArchived(_ workerID: String, _ value: Bool) async {
        guard let project = selectedProjectID else { return }
        do {
            try await client.setWorkerArchived(project, workerID, archived: value)
            await loadArchived()
        } catch { errorMessage = "归档：\(error.localizedDescription)" }
    }

    func removeWorker(_ workerID: String) async {
        guard let project = selectedProjectID else { return }
        do {
            try await client.deleteWorker(project, workerID)
            workerDetail = nil
            await reloadWorkers()
            await loadArchived()
        } catch { errorMessage = "删除：\(error.localizedDescription)" }
    }

    @discardableResult
    func createWorker(_ body: [String: Any]) async -> Bool {
        guard let project = selectedProjectID else { return false }
        do {
            _ = try await client.createWorker(project, body: body)
            await reloadWorkers()
            return true
        } catch {
            errorMessage = "创建工作单元：\(error.localizedDescription)"
            return false
        }
    }

    func updateWorkerMeta(_ workerID: String, label: String, tags: [String], description: String) async -> Bool {
        guard let project = selectedProjectID else { return false }
        do {
            try await client.updateWorkerMeta(project, workerID, label: label, tags: tags, description: description)
            await reloadWorkers()
            return true
        } catch {
            errorMessage = "保存备注：\(error.localizedDescription)"
            return false
        }
    }

    func updateWorkerParams(_ workerID: String, params: JSONValue) async -> Bool {
        guard let project = selectedProjectID else { return false }
        do {
            try await client.updateWorkerParams(project, workerID, params: params)
            await reloadWorkers()
            return true
        } catch {
            errorMessage = "保存参数：\(error.localizedDescription)"
            return false
        }
    }

    func updateWorkerAllow(_ workerID: String,
                           publish: [PublishPattern], subscribe: [EventPattern]) async -> Bool {
        guard let project = selectedProjectID else { return false }
        do {
            try await client.updateWorkerAllow(project, workerID, publish: publish, subscribe: subscribe)
            await reloadWorkers()
            return true
        } catch {
            errorMessage = "保存许可：\(error.localizedDescription)"
            return false
        }
    }

    func loadWorkerContext(_ workerID: String) async {
        guard let project = selectedProjectID else { return }
        if let status = try? await client.workerContext(project, workerID) {
            workerContexts[workerID] = status
        }
    }

    func loadWorkerMounts(_ workerID: String) async {
        guard let project = selectedProjectID else { return }
        if let mounts = try? await client.workerMounts(project, workerID) {
            workerMounts[workerID] = mounts
        }
    }

    func mutateMount(_ workerID: String, add: Bool, path: String) async -> String? {
        guard let project = selectedProjectID else { return nil }
        do {
            let reply = try await client.mutateMount(project, workerID, add: add, path: path)
            if let done = reply.object?["done"]?.bool, done == false {
                return reply.object?["error"]?.string ?? "操作失败"
            }
            await loadWorkerMounts(workerID)
            return nil
        } catch { return error.localizedDescription }
    }

    // MARK: Programs

    func loadPrograms() async {
        guard let project = selectedProjectID else { return }
        do { programs = try await client.programs(project) }
        catch { log("programs:", error.localizedDescription) }
    }

    func programDetail(_ name: String) async -> ProgramDetail? {
        guard let project = selectedProjectID else { return nil }
        do { return try await client.program(project, name) }
        catch {
            errorMessage = "读取程序：\(error.localizedDescription)"
            return nil
        }
    }

    func programContent(_ name: String, path: String) async -> ProgramContent? {
        guard let project = selectedProjectID else { return nil }
        return try? await client.programContent(project, name, path: path)
    }

    @discardableResult
    func saveProgram(_ name: String, contentType: String?, description: String?, tags: [String]?, body: String) async -> Bool {
        guard let project = selectedProjectID else { return false }
        do {
            try await client.updateProgram(project, name, contentType: contentType,
                                           description: description, tags: tags, body: body)
            await loadPrograms()
            return true
        } catch {
            errorMessage = "保存程序：\(error.localizedDescription)"
            return false
        }
    }

    func removeProgram(_ name: String) async {
        guard let project = selectedProjectID else { return }
        do {
            try await client.deleteProgram(project, name)
            await loadPrograms()
        } catch { errorMessage = "删除程序：\(error.localizedDescription)" }
    }

    // MARK: Templates & providers (control plane)

    func loadTemplates() async {
        do { templates = try await client.templates() }
        catch { errorMessage = "模板：\(error.localizedDescription)" }
    }

    func templateDocument(_ name: String) async -> JSONValue? {
        do { return try await client.template(name) }
        catch {
            errorMessage = "读取模板：\(error.localizedDescription)"
            return nil
        }
    }

    @discardableResult
    func saveTemplate(id: String, document: JSONValue, existing: Bool) async -> Bool {
        do {
            if existing { try await client.updateTemplate(name: id, template: document) }
            else { try await client.createTemplate(id: id, template: document) }
            await loadTemplates()
            return true
        } catch {
            errorMessage = "保存模板：\(error.localizedDescription)"
            return false
        }
    }

    func removeTemplate(_ name: String) async {
        do {
            try await client.deleteTemplate(name: name)
            await loadTemplates()
        } catch { errorMessage = "删除模板：\(error.localizedDescription)" }
    }

    func importTemplate(id: String, fileURL: URL) async -> Bool {
        do {
            try await client.importTemplate(id: id, fileURL: fileURL)
            await loadTemplates()
            return true
        } catch {
            errorMessage = "导入模板：\(error.localizedDescription)"
            return false
        }
    }

    func loadProvidersDoc() async {
        do { providersDoc = try await client.providersRaw() }
        catch { log("providers doc:", error.localizedDescription) }
    }

    @discardableResult
    func saveProviders(_ document: JSONValue) async -> Bool {
        do {
            try await client.updateProviders(document)
            providersDoc = document
            await loadProviders()
            return true
        } catch {
            errorMessage = "保存模型配置：\(error.localizedDescription)"
            return false
        }
    }

    // MARK: Control plane — project creation

    func createProject(id: String, template: String) async -> Bool {
        busy = true
        defer { busy = false }
        do {
            try await client.createProject(id: id, template: template)
            await loadProjects()
            selectedProjectID = id
            await connectSelected()
            return true
        } catch {
            errorMessage = "创建项目：\(error.localizedDescription)"
            return false
        }
    }

    func linkProject(path: String) async -> Bool {
        do {
            let project = try await client.linkProject(path: path)
            await loadProjects()
            selectedProjectID = project.id
            await connectSelected()
            return true
        } catch {
            errorMessage = "链接目录：\(error.localizedDescription)"
            return false
        }
    }

    func projectDirs(_ path: String?) async -> [GatewayClient.DirEntry] {
        (try? await client.projectDirs(path)) ?? []
    }

    private func log(_ items: Any...) {
        let text = "[niq] " + items.map { "\($0)" }.joined(separator: " ")
        FileHandle.standardError.write(Data((text + "\n").utf8))
    }
}
