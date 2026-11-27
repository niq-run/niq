import Foundation

/// A value decoded from the bus / project API without narrowing its shape.
/// Payloads are owned by event type, so the wire layer must not narrow them.
enum JSONValue: Codable, Hashable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let n = try? c.decode(Double.self) { self = .number(n); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([JSONValue].self) { self = .array(a); return }
        self = .object(try c.decode([String: JSONValue].self))
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }

    var string: String? { if case .string(let s) = self { return s }; return nil }
    var object: [String: JSONValue]? { if case .object(let o) = self { return o }; return nil }
    var array: [JSONValue]? { if case .array(let a) = self { return a }; return nil }
    var number: Double? { if case .number(let n) = self { return n }; return nil }
    var bool: Bool? { if case .bool(let b) = self { return b }; return nil }
    var isEmpty: Bool {
        switch self {
        case .null: return true
        case .string(let s): return s.isEmpty
        case .array(let a): return a.isEmpty
        case .object(let o): return o.isEmpty
        default: return false
        }
    }

    /// Re-serialized JSON, pretty-printed by default. Used by the event/tool
    /// inspectors so a payload reads as JSON rather than Swift's description.
    func jsonText(pretty: Bool = true) -> String {
        let encoder = JSONEncoder()
        if pretty {
            encoder.outputFormatting = [.prettyPrinted, .withoutEscapingSlashes, .sortedKeys]
        } else {
            encoder.outputFormatting = [.withoutEscapingSlashes]
        }
        if let data = try? encoder.encode(self), let text = String(data: data, encoding: .utf8) {
            return text
        }
        return ""
    }
}

struct Ports: Codable, Hashable {
    var webui: Int?
    var bus: Int?
}

/// One project as `GET /api/projects` lists it (the gateway's `contract.View`).
struct ProjectView: Decodable, Identifiable, Hashable {
    var id: String
    var running: Bool?
    var ports: Ports?
    var dir: String?
    var createdAt: String?

    enum CodingKeys: String, CodingKey {
        case id, running, ports, dir
        case createdAt = "created_at"
    }
}

/// One identity on a project's bus (`GET /p/{id}/api/workers`).
struct Worker: Decodable, Identifiable, Hashable {
    var id: String
    var type: String?
    var label: String?
    var online: Bool?
    var managed: Bool?
    var remote: Bool?
    var placement: String?
    var state: String?
    var unmanagedState: String?
    var description: String?
    var tags: [String]?
    var params: [String: JSONValue]?
    var credential: String?
    var publishAllow: [PublishPattern]?
    var subscribeAllow: [EventPattern]?

    enum CodingKeys: String, CodingKey {
        case id, type, label, online, managed, remote, placement, state
        case description, tags, params, credential
        case unmanagedState = "unmanaged_state"
        case publishAllow = "publish_allow"
        case subscribeAllow = "subscribe_allow"
    }

    var displayName: String { (label?.isEmpty == false ? label! : id) }
    var hasRename: Bool { !(label ?? "").isEmpty }
    var isTalkPartner: Bool { ["reason", "coding", "niq"].contains(type ?? "") }
    var isReasonFamily: Bool { ["reason", "coding"].contains(type ?? "") }
    var isWorkspace: Bool { (type ?? "") == "workspace" }
    var isMCP: Bool { (type ?? "") == "mcp" }

    /// Where the worker runs, as the WebUI's 托管 column names it.
    var placementLabel: String {
        if remote == true { return "三方" }
        switch placement ?? "" {
        case "in-process": return "托管"
        case "managed-process": return "子进程"
        case "unmanaged-process": return "三方"
        default: break
        }
        if managed == true { return "托管" }
        if managed == false { return "子进程" }
        return "—"
    }

    /// Lifecycle wording: running / suspended / stopped, else a dash.
    var stateLabel: String {
        switch state ?? "" {
        case "running": return "运行中"
        case "suspended": return "已休眠"
        case "stopped": return "已停止"
        case "": return "—"
        default: return state!
        }
    }

    var canSuspend: Bool { managed == true && (state ?? "running") == "running" }
    var canResume: Bool { managed == true && (state ?? "") == "suspended" }
    var canStop: Bool { (state ?? "running") != "stopped" }
    var canStart: Bool { (state ?? "") == "stopped" }
}

/// One bus event as the project's SSE stream delivers it.
struct Event: Decodable, Identifiable, Hashable {
    var id: String
    var type: String
    var workerID: String?
    var targetWorkerID: String?
    var requestID: String?
    var traceID: String?
    var timestamp: Int64?
    var payload: [String: JSONValue]?
    var transient: Bool?

    var recipients: [String]?

    enum CodingKeys: String, CodingKey {
        case id, type, payload, transient, timestamp, recipients
        case workerID = "worker_id"
        case targetWorkerID = "target_worker_id"
        case requestID = "request_id"
        case traceID = "trace_id"
    }

    /// A short text summary for a transcript row, without narrowing the payload.
    var summary: String {
        guard let payload else { return "" }
        if let text = payload["text"]?.string, !text.isEmpty { return text }
        if let content = payload["content"]?.array {
            let texts = content.compactMap { $0.object?["text"]?.string }
            if !texts.isEmpty { return texts.joined(separator: "\n") }
        }
        return ""
    }

    var date: Date? { timestamp.map { Date(timeIntervalSince1970: TimeInterval($0)) } }

    /// `HH:mm:ss`, matching the WebUI's row timestamps.
    var timeText: String {
        guard let date else { return "" }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "HH:mm:ss"
        return f.string(from: date)
    }

    var dateTimeText: String {
        guard let date else { return "" }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd HH:mm:ss"
        return f.string(from: date)
    }

    /// Terminal answer to a tool call (`request.completed|failed|rejected`).
    var isToolResult: Bool {
        ["request.completed", "request.failed", "request.rejected"].contains(type)
    }

    var isReasonBoundary: Bool { type == "reason.start" || type == "reason.end" }

    /// A delta/partial stream event, never rendered as a standalone row.
    var isDelta: Bool {
        type == "reason.text_delta" || type == "reason.thinking_delta" || type == "request.progressed"
    }

    /// Every worker id this envelope touches, for scope filtering.
    var involvedWorkers: Set<String> {
        var out = Set<String>()
        if let workerID, !workerID.isEmpty { out.insert(workerID) }
        if let targetWorkerID, !targetWorkerID.isEmpty { out.insert(targetWorkerID) }
        for r in recipients ?? [] { out.insert(r) }
        return out
    }
}

/// One element of a structured input's content array (llm-like blocks).
struct Block: Codable, Hashable {
    var type: String
    var text: String?
    var name: String?
    var mime: String?
    var data: String?
    var path: String?
    var size: Int64?
}

struct InputBody: Encodable {
    var content: [Block]
    var targets: [String]
    var target: String?
    var input_mode: String?
}

struct Instance: Decodable, Hashable {
    var webui: Int?
    var bus: Int?
    var already: Bool?
}

/// The `niq status --json` snapshot, decoded leniently for the menu bar.
struct StatusSnapshot: Decodable {
    var version: String?
    var gateway: Gateway
    var projects: [Project]

    struct Gateway: Decodable {
        var running: Bool?
        var pid: Int?
        var url: String?
        var log: String?
        var started: String?
    }

    struct Project: Decodable {
        var id: String
        var running: Bool?
        var webui: Int?
        var bus: Int?
        var url: String?
        var log: String?
    }
}

/// The model-provider catalog (`GET /api/providers` on the gateway).
struct ProviderConfig: Decodable {
    var active: String?
    var providers: [ProviderInfo]?
}

/// One selectable model provider of a reason worker, as the worker reports it
/// via `GET /p/{id}/api/workers/{wid}/providers`.
struct ProviderOption: Decodable, Identifiable, Hashable {
    var name: String
    var defaultModel: String?
    var models: [String]?

    var id: String { name }

    enum CodingKeys: String, CodingKey {
        case name, models
        case defaultModel = "default"
    }
}

struct ProviderSelection: Decodable, Hashable {
    var provider: String?
    var model: String?
}

struct WorkerProviders: Decodable, Hashable {
    var providers: [ProviderOption]?
    var current: ProviderSelection?
}

/// One approval request the project's approver tracks (`GET /p/{id}/api/approvals`).
struct ApprovalDecision: Decodable, Hashable {
    var approved: Bool?
    var note: String?
    var timestamp: Int64?
}

struct ApprovalEntry: Decodable, Identifiable, Hashable {
    var id: String
    var requestID: String?
    var workerID: String?
    var action: String?
    var tool: String?
    var path: String?
    var traceID: String?
    var timestamp: Int64?
    var payload: [String: JSONValue]?
    var decision: ApprovalDecision?

    enum CodingKeys: String, CodingKey {
        case id, action, tool, path, timestamp, payload, decision
        case requestID = "request_id"
        case workerID = "worker_id"
        case traceID = "trace_id"
    }

    var timeText: String {
        guard let timestamp else { return "" }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "MM-dd HH:mm:ss"
        return f.string(from: Date(timeIntervalSince1970: TimeInterval(timestamp)))
    }

    var isPending: Bool { decision?.approved == nil }

    /// What the request wants, in one line.
    var title: String {
        if let action, !action.isEmpty { return action }
        if let tool, !tool.isEmpty { return tool }
        return "审批请求"
    }
}

struct ApprovalList: Decodable {
    var approvals: [ApprovalEntry]?
}

/// One staged attachment in the composer. Images ride inline as base64; files
/// are uploaded first and referenced by path (the WebUI's two shapes).
struct StagedAttachment: Identifiable, Hashable {
    enum Kind: String { case image, file }
    let id: String
    let kind: Kind
    let name: String
    let mime: String?
    let data: String?
    let path: String?
    let size: Int64?
}

/// `POST /p/{id}/api/upload` → the stored file's absolute path.
struct UploadedFile: Decodable, Hashable {
    var path: String
    var name: String?
    var size: Int64?
}

// MARK: - Bus-driven worker capabilities

/// One capability a worker declared in its `worker.ready` watch list.
struct WatchEntry: Decodable, Hashable, Identifiable {
    var event: String
    var desc: String?
    var parameters: JSONValue?
    var id: String { event }
}

/// A worker's publish grant: an event type, optionally to one target.
struct PublishPattern: Codable, Hashable, Identifiable {
    var type: String
    var target: String?
    var id: String { type + "|" + (target ?? "*") }
}

/// A worker's subscribe grant: an event type, optionally from one source.
struct EventPattern: Codable, Hashable, Identifiable {
    var type: String
    var source: String?
    var id: String { type + "|" + (source ?? "*") }
}

/// The answer to a bus `ask` (a worker event + its terminal reply).
struct AskReply {
    var ok: Bool
    var result: JSONValue?
    var error: String?

    /// The reply as tool-card text (string stays text, object → JSON).
    var text: String {
        guard let result else { return error ?? "" }
        if let s = result.string { return s }
        return result.jsonText(pretty: true)
    }
}

// MARK: - Programs / templates / mounts

struct ProgramInfo: Decodable, Identifiable, Hashable {
    var name: String
    var contentType: String?
    var formType: String?
    var description: String?
    var tags: [String]?
    var locked: Bool?
    var contents: Int?

    var id: String { name }

    enum CodingKeys: String, CodingKey {
        case name, description, tags, locked, contents
        case contentType = "content_type"
        case formType = "form_type"
    }
}

struct ProgramDetail: Decodable, Hashable {
    var name: String
    var contentType: String?
    var formType: String?
    var description: String?
    var tags: [String]?
    var locked: Bool?
    var body: String
    var contents: [String]?

    enum CodingKeys: String, CodingKey {
        case name, description, tags, locked, body, contents
        case contentType = "content_type"
        case formType = "form_type"
    }
}

struct ProgramContent: Decodable, Hashable {
    var name: String
    var body: String
}

struct MountList: Decodable, Hashable {
    var mounts: [String]?
    var primary: String?
}

struct WorkerContextStatus: Decodable, Hashable {
    var contextWindow: Int?
    var transcriptTokens: Int?
    var messages: Int?
    var rounds: Int?
    var usedTokens: Int?
    var inputTokens: Int?
    var outputTokens: Int?
    var totalTokens: Int?
    var cacheRead: Int?
    var cacheCreation: Int?
    var cacheHitPercent: Double?
    var model: String?

    enum CodingKeys: String, CodingKey {
        case messages, rounds, model
        case contextWindow = "context_window"
        case transcriptTokens = "transcript_tokens"
        case usedTokens = "used_tokens"
        case inputTokens = "input_tokens"
        case outputTokens = "output_tokens"
        case totalTokens = "total_tokens"
        case cacheRead = "cache_read_tokens"
        case cacheCreation = "cache_creation_tokens"
        case cacheHitPercent = "cache_hit_percent"
    }

    /// Reuse the transcript panel's usage rendering.
    var usage: UsageInfo {
        var info = UsageInfo()
        info.contextWindow = contextWindow
        info.inputTokens = inputTokens
        info.outputTokens = outputTokens
        info.totalTokens = totalTokens ?? usedTokens
        info.cacheRead = cacheRead
        info.cacheCreation = cacheCreation
        info.cacheHitPercent = cacheHitPercent
        info.cacheReported = cacheRead != nil
        info.model = model
        return info
    }
}

/// Identifiable wrapper so a worker id can drive `.sheet(item:)`.
struct WorkerRef: Identifiable, Hashable {
    let id: String
}

struct ProviderInfo: Decodable, Identifiable, Hashable {
    var name: String
    var type: String?
    var base_url: String?
    var model: String?
    var models: [JSONValue]?

    var id: String { name }

    /// `models` entries are either a bare name string or an object with `name`.
    var modelNames: [String] {
        (models ?? []).compactMap { entry in
            if let s = entry.string { return s }
            if let n = entry.object?["name"]?.string { return n }
            return nil
        }
    }
}

/// Token accounting carried by a `reason.response` (`payload.meta.usage`).
struct UsageInfo: Hashable {
    /// One part of the round's prompt, as the worker reports it in
    /// `meta.usage.breakdown`.
    struct Section: Hashable, Identifiable {
        let key: String
        let tokens: Int
        var id: String { key }

        var label: String {
            switch key {
            case "system": return "系统提示词"
            case "program", "program.instructions": return "指令"
            case "program.playbooks": return "剧本"
            case "tools", "tools.builtin": return "内置工具"
            case "tools.program": return "程序 worker"
            case "tools.mcp": return "MCP / 连接器"
            case "tools.workers": return "其他 worker"
            case "tools.other": return "未归类工具"
            case "messages", "messages.user": return "用户与系统输入"
            case "messages.assistant": return "模型文本"
            case "messages.tool_calls", "toolCalls.calls": return "调用参数"
            case "messages.tool_results", "toolCalls.results": return "调用结果"
            case "messages.thinking", "thinking": return "思考过程"
            case "output": return "本轮输出"
            default: return key
            }
        }
    }

    var contextWindow: Int?
    var inputTokens: Int?
    var outputTokens: Int?
    var totalTokens: Int?
    var cacheRead: Int?
    var cacheCreation: Int?
    var cacheHitPercent: Double?
    var cacheReported: Bool = false
    var model: String?
    var breakdown: [Section] = []

    static func parse(_ payload: [String: JSONValue]?) -> UsageInfo? {
        guard let usage = payload?["meta"]?.object?["usage"]?.object else { return nil }
        func int(_ key: String) -> Int? { if case .number(let n)? = usage[key] { return Int(n) }; return nil }
        func dbl(_ key: String) -> Double? { if case .number(let n)? = usage[key] { return n }; return nil }
        var info = UsageInfo()
        info.contextWindow = int("context_window") ?? int("contextWindow")
        info.inputTokens = int("input_tokens") ?? int("prompt_tokens")
        info.outputTokens = int("output_tokens") ?? int("completion_tokens")
        info.totalTokens = int("total_tokens")
        info.cacheRead = int("cache_read_tokens")
        info.cacheCreation = int("cache_creation_tokens")
        info.cacheHitPercent = dbl("cache_hit_percent")
        info.cacheReported = usage["cache_read_tokens"] != nil
        info.model = usage["model"]?.string
        info.breakdown = (usage["breakdown"]?.array ?? []).compactMap { entry in
            guard let object = entry.object,
                  let key = object["key"]?.string, !key.isEmpty,
                  let tokens = object["tokens"]?.number, tokens > 0 else { return nil }
            return Section(key: key, tokens: Int(tokens))
        }
        return info
    }

    /// Occupancy is measured against the round's whole prompt (input + output),
    /// matching the WebUI's `total / context_window`.
    var contextPercent: Double? {
        guard let window = contextWindow, window > 0 else { return nil }
        let used = totalTokens ?? ((inputTokens ?? 0) + (outputTokens ?? 0))
        return Double(used) / Double(window) * 100
    }

    /// The left half of the usage line: window + occupancy + model.
    var leftPart: String {
        var parts: [String] = []
        if let window = contextWindow, let pct = contextPercent {
            parts.append("上下文窗口 \(UsageInfo.compact(window)) · 已用 \(UsageInfo.percent(pct))%")
        }
        if let model, !model.isEmpty { parts.append(model) }
        return parts.joined(separator: " · ")
    }

    /// The right half: this round's input / output / cache accounting.
    var rightPart: String {
        var parts: [String] = []
        if let inputTokens { parts.append("输入 \(UsageInfo.compact(inputTokens))") }
        if let outputTokens { parts.append("输出 \(UsageInfo.compact(outputTokens))") }
        if cacheReported, let read = cacheRead {
            let pct = cacheHitPercent.map { UsageInfo.percent($0) } ?? "0"
            parts.append("缓存 \(UsageInfo.compact(read)) (\(pct)%)")
        }
        return parts.joined(separator: " · ")
    }

    var hasParts: Bool { !leftPart.isEmpty || !rightPart.isEmpty }

    /// One-line rendering for tight spaces.
    var summary: String {
        [leftPart, rightPart].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// 128000 → "128K", 1290 → "1K", 900 → "900".
    static func compact(_ n: Int) -> String {
        n < 1000 ? "\(n)" : "\(Int((Double(n) / 1000).rounded()))K"
    }

    /// Keeps one decimal below 10% so a small occupancy is not "0".
    static func percent(_ p: Double) -> String {
        if p == 0 { return "0" }
        return p < 10 ? String(format: "%.1f", p) : "\(Int(p.rounded()))"
    }
}
