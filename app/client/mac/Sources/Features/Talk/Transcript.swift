import Foundation

// The conversation view's projection of the bus.
//
// The WebUI renders a talk row per event: worker.input → a message bubble,
// reason.response → a reply card, reason.thinking → a disclosure, and every
// other invocation → a tool card whose terminal `request.*` reply is merged in
// as its result. A run of process rows (thinking, tool cards, interruptions)
// folds into one chip when 折叠过程事件 is on, so a turn reads
// input → [N 条过程事件] → reply.
//
// This file owns that projection only — no SwiftUI — so the view stays a thin
// renderer and the grouping rules are testable in isolation.

// MARK: - Items

struct AttachmentRef: Identifiable, Hashable {
    enum Kind: String { case image, file }
    let id: String
    let kind: Kind
    let name: String
    let mime: String?
    /// Base64 payload for images (the WebUI embeds picked images inline).
    let data: String?
    let path: String?
}

struct HumanItem: Identifiable, Hashable {
    let id: String
    let workerID: String
    /// "我[webui]" for the human's own channel, else the sender's display name.
    let sender: String
    let targets: [String]
    let targetNames: [String]
    let mode: String?
    let text: String
    /// A `<system-reminder>` envelope the input carried, shown apart from prose.
    let reminder: String?
    let time: String
    let trace: String?
    let attachments: [AttachmentRef]

    var alignRight: Bool { workerID == Transcript.humanID }
}

struct AssistantItem: Identifiable, Hashable {
    let id: String
    let workerID: String
    let name: String
    let text: String
    let usage: UsageInfo?
    let trace: String?
    let quoteText: String?
    let quoteWorker: String?
    let quoteEventID: String?
    let time: String

    var alignRight: Bool { false }
}

struct ThinkingItem: Identifiable, Hashable {
    let id: String
    let workerID: String
    let name: String
    let text: String
    let time: String

    var alignRight: Bool { false }
}

enum ToolStatus: String, Hashable {
    case pending, completed, failed, rejected

    var color: ToolTone { switch self { case .pending: return .pending; case .completed: return .completed; case .failed: return .failed; case .rejected: return .neutral } }
}

enum ToolTone { case pending, completed, failed, neutral }

struct ToolItem: Identifiable, Hashable {
    let id: String
    let workerID: String
    let name: String
    let type: String
    /// "to X" / "from: X" — the WebUI's direction segment.
    let direction: String?
    let source: String?
    let args: String?
    let result: String?
    let chars: Int
    let time: String
    let status: ToolStatus

    var alignRight: Bool { false }
}

enum NoticeKind: String, Hashable {
    case abort, timeout, cancel, interrupted, approval

    /// The rows the WebUI's fold mode never hides.
    var keepsRow: Bool { self == .abort || self == .timeout || self == .approval }

    var label: String {
        switch self {
        case .abort: return "已中止"
        case .timeout: return "已超时"
        case .cancel: return "请求已取消"
        case .interrupted: return "推理被打断"
        case .approval: return "审批请求"
        }
    }
}

struct NoticeItem: Identifiable, Hashable {
    let id: String
    let kind: NoticeKind
    let workerID: String
    let name: String
    let text: String
    let time: String

    var alignRight: Bool { false }
    var keepsRow: Bool { kind.keepsRow }
}

enum TalkItem: Identifiable, Hashable {
    case human(HumanItem)
    case assistant(AssistantItem)
    case thinking(ThinkingItem)
    case tool(ToolItem)
    case notice(NoticeItem)

    var id: String {
        switch self {
        case .human(let i): return i.id
        case .assistant(let i): return i.id
        case .thinking(let i): return i.id
        case .tool(let i): return i.id
        case .notice(let i): return i.id
        }
    }

    var workerID: String {
        switch self {
        case .human(let i): return i.workerID
        case .assistant(let i): return i.workerID
        case .thinking(let i): return i.workerID
        case .tool(let i): return i.workerID
        case .notice(let i): return i.workerID
        }
    }

    var alignRight: Bool {
        switch self {
        case .human(let i): return i.alignRight
        case .assistant(let i): return i.alignRight
        case .thinking(let i): return i.alignRight
        case .tool(let i): return i.alignRight
        case .notice(let i): return i.alignRight
        }
    }

    /// Whether this row survives 仅回复 mode.
    var isResponseContent: Bool {
        switch self {
        case .human, .assistant: return true
        case .notice(let i): return i.keepsRow
        default: return false
        }
    }
}

// MARK: - Layout

/// One laid-out row: the item plus whether it opens a new speaker run (the big
/// blue name above a block).
struct RowItem: Identifiable, Hashable {
    let item: TalkItem
    let showHeader: Bool
    var id: String { item.id }
}

/// A run of folded process rows.
struct RunBlock: Identifiable, Hashable {
    let id: String
    let workerID: String
    let alignRight: Bool
    let showHeader: Bool
    let tools: Int
    let thinking: Int
    let other: Int
    var rows: [RowItem]

    var total: Int { rows.count }
}

enum TalkBlock: Identifiable, Hashable {
    case row(RowItem)
    case run(RunBlock)

    var id: String {
        switch self {
        case .row(let r): return r.id
        case .run(let r): return r.id
        }
    }
}

// MARK: - Options

struct TranscriptOptions {
    /// Sidebar worker checkboxes: which workers' traffic is in this conversation.
    var filter: Set<String> = []
    /// The project's talk partners (reason / coding / niq). When no filter is
    /// checked, the conversation is scoped to these — matching the WebUI, which
    /// keeps workspace→UI plumbing out of a talk with no selection.
    var partners: Set<String> = []
    var responseOnly = false
    /// 折叠过程事件
    var collapse = true
    var names: [String: String] = [:]
    var humanID: String = Transcript.humanID
}

// MARK: - Builder

enum Transcript {
    static let humanID = "webui"

    /// Worker types that consume `worker.input` — the conversation partners.
    static let partnerTypes: Set<String> = ["reason", "coding", "niq"]

    private static let skippedTypes: Set<String> = [
        "reason.start", "reason.end", "session.start", "session.end",
        "worker.ready", "worker.hello",
    ]

    // MARK: Items

    static func build(_ events: [Event], options: TranscriptOptions) -> [TalkItem] {
        // One pass pairs every terminal reply with the invocation it answers,
        // and records the streaming partial of a call still in flight.
        var results: [String: Event] = [:]
        var partials: [String: String] = [:]
        for event in events {
            guard let requestID = event.requestID, !requestID.isEmpty else { continue }
            if event.isToolResult {
                results[requestID] = event
            } else if event.type == "request.progressed",
                      let partial = event.payload?["partial"]?.string, !partial.isEmpty {
                partials[requestID] = partial
            }
        }

        var items: [TalkItem] = []
        // The quote a reply shows is the last input with the same trace before it.
        var inputByTrace: [String: (text: String, worker: String, id: String)] = [:]

        for event in events where !event.isDelta && !event.isReasonBoundary {
            guard inScope(event, filter: options.filter, partners: options.partners) else { continue }
            if skippedTypes.contains(event.type) { continue }
            if event.type.hasPrefix("worker."),
               event.type != "worker.input", event.type != "worker.abort",
               event.type != "worker.help", event.type != "worker.readme",
               event.type != "worker.readme.update" {
                continue
            }
            // The detail panel's own readme fetch is UI housekeeping, not talk.
            if event.type == "worker.readme", event.workerID == options.humanID { continue }
            if options.responseOnly, !isResponseContent(event.type) { continue }

            switch event.type {
            case "worker.input":
                guard let human = humanItem(event, options: options) else { continue }
                if let trace = event.traceID, !human.text.isEmpty {
                    inputByTrace[trace] = (human.text, event.workerID ?? "", event.id)
                }
                items.append(.human(human))

            case "reason.response":
                guard let reply = assistantItem(event, inputByTrace: inputByTrace, options: options) else { continue }
                items.append(.assistant(reply))

            case "reason.thinking":
                guard let text = text(of: event.payload), !text.isEmpty else { continue }
                items.append(.thinking(ThinkingItem(
                    id: event.id,
                    workerID: event.workerID ?? "",
                    name: options.names[event.workerID ?? ""] ?? (event.workerID ?? ""),
                    text: text,
                    time: event.timeText
                )))

            case "worker.abort":
                items.append(.notice(notice(event, kind: .abort, text: "已请求停止", options: options)))
            case "timer.timeout":
                let target = event.targetWorkerID.flatMap { options.names[$0] ?? $0 } ?? ""
                items.append(.notice(notice(event, kind: .timeout,
                                            text: target.isEmpty ? "等待超时" : "等待 \(target) 超时",
                                            options: options)))
            case "request.cancel":
                items.append(.notice(notice(event, kind: .cancel, text: "请求已取消", options: options)))
            case "reason.interrupted":
                let reason = event.payload?["reason"]?.string ?? "推理被打断"
                items.append(.notice(notice(event, kind: .interrupted, text: reason, options: options)))
            case "approval.request":
                let action = event.payload?["action"]?.string ?? event.payload?["tool"]?.string ?? "审批请求"
                items.append(.notice(notice(event, kind: .approval, text: action, options: options)))

            default:
                // Terminal replies are merged into their invocation card.
                if event.isToolResult { continue }
                guard let tool = toolItem(event, result: results[event.requestID ?? ""],
                                          partial: partials[event.requestID ?? ""], options: options) else { continue }
                items.append(.tool(tool))
            }
        }
        return items
    }

    // MARK: Layout (folding + speaker runs)

    static func layout(_ items: [TalkItem], collapse: Bool) -> [TalkBlock] {
        var blocks: [TalkBlock] = []
        var lastSpeaker = ""
        var run: RunBlock? = nil
        var showHeaderForRun = false

        func speakerHead(_ item: TalkItem) -> Bool {
            let head = item.workerID != lastSpeaker
            if head { lastSpeaker = item.workerID }
            return head
        }

        func flush() {
            if let run { blocks.append(.run(run)) }
            run = nil
        }

        for item in items {
            let fold = collapse && !keepVisible(item)
            if !fold {
                flush()
                blocks.append(.row(RowItem(item: item, showHeader: speakerHead(item))))
                continue
            }
            if run != nil, run!.workerID != item.workerID || run!.alignRight != item.alignRight {
                flush()
            }
            if run == nil {
                showHeaderForRun = speakerHead(item)
                run = RunBlock(id: "run-\(item.id)", workerID: item.workerID,
                               alignRight: item.alignRight, showHeader: showHeaderForRun,
                               tools: 0, thinking: 0, other: 0, rows: [])
            }
            var current = run!
            current.rows.append(RowItem(item: item, showHeader: false))
            switch item {
            case .thinking: current = rebuild(current, thinking: current.thinking + 1)
            case .tool: current = rebuild(current, tools: current.tools + 1)
            default: current = rebuild(current, other: current.other + 1)
            }
            run = current
        }
        flush()
        return blocks
    }

    private static func rebuild(_ run: RunBlock, tools: Int? = nil, thinking: Int? = nil, other: Int? = nil) -> RunBlock {
        RunBlock(id: run.id, workerID: run.workerID, alignRight: run.alignRight,
                 showHeader: run.showHeader,
                 tools: tools ?? run.tools, thinking: thinking ?? run.thinking, other: other ?? run.other,
                 rows: run.rows)
    }

    // MARK: Item builders

    private static func humanItem(_ event: Event, options: TranscriptOptions) -> HumanItem? {
        let parsed = parseInput(event.payload)
        guard !parsed.text.isEmpty || !parsed.attachments.isEmpty else { return nil }
        let split = splitSystemReminder(parsed.text)
        let workerID = event.workerID ?? ""
        let sender = workerID == options.humanID
            ? "我[\(options.humanID)]"
            : (options.names[workerID] ?? workerID)
        var targets: [String] = []
        if let target = event.targetWorkerID, !target.isEmpty { targets = [target] }
        else if let recipients = event.recipients, !recipients.isEmpty { targets = recipients }
        return HumanItem(
            id: event.id,
            workerID: workerID,
            sender: sender,
            targets: targets,
            targetNames: targets.map { options.names[$0] ?? $0 },
            mode: event.payload?["input_mode"]?.string,
            text: split.content,
            reminder: split.reminder,
            time: event.timeText,
            trace: event.traceID,
            attachments: parsed.attachments
        )
    }

    private static func assistantItem(_ event: Event,
                                      inputByTrace: [String: (text: String, worker: String, id: String)],
                                      options: TranscriptOptions) -> AssistantItem? {
        guard let text = text(of: event.payload), !text.isEmpty else { return nil }
        let workerID = event.workerID ?? ""
        let quote = event.traceID.flatMap { inputByTrace[$0] }
        return AssistantItem(
            id: event.id,
            workerID: workerID,
            name: options.names[workerID] ?? workerID,
            text: text,
            usage: UsageInfo.parse(event.payload),
            trace: event.traceID,
            quoteText: quote?.text,
            quoteWorker: quote.map { $0.worker == options.humanID ? "我" : (options.names[$0.worker] ?? $0.worker) },
            quoteEventID: quote?.id,
            time: event.timeText
        )
    }

    private static func notice(_ event: Event, kind: NoticeKind, text: String, options: TranscriptOptions) -> NoticeItem {
        let workerID = event.workerID ?? ""
        return NoticeItem(id: event.id, kind: kind, workerID: workerID,
                          name: options.names[workerID] ?? workerID,
                          text: text, time: event.timeText)
    }

    private static func toolItem(_ event: Event, result: Event?, partial: String?, options: TranscriptOptions) -> ToolItem? {
        let workerID = event.workerID ?? ""
        // A code-running call is read as code; the rest of the payload is args.
        let source = toolSource(event)
        let args = source != nil ? toolArgsJSON(event, pretty: true) : nil
        let resultText = result.map { toolResultText($0) } ?? (partial ?? "")
        let argsText = source == nil ? toolPayloadJSON(event, pretty: true) : args

        // Nothing to show and no reply yet: a bare lifecycle event, skip it.
        if source == nil, (argsText ?? "").isEmpty, resultText.isEmpty, event.requestID == nil { return nil }

        let direction: String?
        if let target = event.targetWorkerID, !target.isEmpty, target != workerID {
            direction = "to \(options.names[target] ?? target)"
        } else if !workerID.isEmpty {
            direction = "from: \(options.names[workerID] ?? workerID)"
        } else {
            direction = nil
        }

        let status: ToolStatus
        if let result {
            switch result.type {
            case "request.completed": status = .completed
            case "request.failed": status = .failed
            case "request.rejected": status = .rejected
            default: status = .pending
            }
        } else {
            status = event.requestID == nil ? .rejected : .pending
        }

        let chars = (source?.count ?? 0) + (argsText?.count ?? 0) + resultText.count
        return ToolItem(
            id: event.id,
            workerID: workerID,
            name: options.names[workerID] ?? workerID,
            type: event.type,
            direction: direction,
            source: source,
            args: source == nil ? nil : argsText,
            result: resultText.isEmpty ? nil : resultText,
            chars: chars,
            time: event.timeText,
            status: status
        )
    }

    // MARK: Scope & visibility

    static func inScope(_ event: Event, filter: Set<String>, partners: Set<String>) -> Bool {
        if !filter.isEmpty {
            return !event.involvedWorkers.isDisjoint(with: filter)
        }
        // No explicit selection: scope to the talk partners (the WebUI's rule).
        // Until the roster loads, show everything rather than nothing.
        if partners.isEmpty { return true }
        if event.type == "approval.request", event.workerID == humanID { return true }
        return !event.involvedWorkers.isDisjoint(with: partners)
    }

    private static func keepVisible(_ item: TalkItem) -> Bool {
        switch item {
        case .human, .assistant: return true
        case .notice(let n): return n.keepsRow
        case .thinking, .tool: return false
        }
    }

    private static func isResponseContent(_ type: String) -> Bool {
        switch type {
        case "worker.input", "worker.abort", "timer.timeout", "reason.response", "approval.request":
            return true
        default:
            return false
        }
    }

    // MARK: Payload reading

    /// Text plus media blocks of a structured input's `content` array.
    static func parseInput(_ payload: [String: JSONValue]?) -> (text: String, attachments: [AttachmentRef]) {
        guard let payload else { return ("", []) }
        if let content = payload["content"]?.array {
            var texts: [String] = []
            var attachments: [AttachmentRef] = []
            for (index, block) in content.enumerated() {
                guard let object = block.object else {
                    if let s = block.string { texts.append(s) }
                    continue
                }
                switch object["type"]?.string ?? "" {
                case "text":
                    if let t = object["text"]?.string { texts.append(t) }
                case "image":
                    attachments.append(AttachmentRef(
                        id: "img-\(index)-\(object["name"]?.string ?? "")",
                        kind: .image,
                        name: object["name"]?.string ?? "图片",
                        mime: object["mime"]?.string,
                        data: object["data"]?.string,
                        path: nil))
                case "file":
                    attachments.append(AttachmentRef(
                        id: "file-\(index)-\(object["name"]?.string ?? "")",
                        kind: .file,
                        name: object["name"]?.string ?? (object["path"]?.string ?? "文件"),
                        mime: object["mime"]?.string,
                        data: nil,
                        path: object["path"]?.string))
                default:
                    if let t = object["text"]?.string { texts.append(t) }
                }
            }
            var text = texts.joined(separator: "\n")
            // A legacy "<attachment>" text envelope still shows its user text.
            if text.isEmpty, let legacy = payload["text"]?.string { text = legacy }
            return (text.trimmingCharacters(in: .whitespacesAndNewlines), attachments)
        }
        if let text = payload["text"]?.string {
            return (text.trimmingCharacters(in: .whitespacesAndNewlines), [])
        }
        return ("", [])
    }

    static func text(of payload: [String: JSONValue]?) -> String? {
        guard let payload else { return nil }
        if let text = payload["text"]?.string, !text.isEmpty { return text }
        if let content = payload["content"]?.array {
            let joined = content.compactMap { block -> String? in
                if let t = block.string { return t }
                if let t = block.object?["text"]?.string { return t }
                return nil
            }.joined(separator: "\n")
            if !joined.isEmpty { return joined }
        }
        if let result = payload["result"]?.string, !result.isEmpty { return result }
        return nil
    }

    // MARK: Tool payloads

    static func toolSource(_ event: Event) -> String? {
        guard !event.isToolResult else { return nil }
        guard event.type == "run" || event.type.hasSuffix(".run") else { return nil }
        guard let payload = event.payload else { return nil }
        if let source = payload["source"]?.string, !source.isEmpty { return source }
        if let script = payload["script"]?.string, !script.isEmpty { return script }
        return nil
    }

    static func toolArgsJSON(_ event: Event, pretty: Bool) -> String? {
        guard let payload = event.payload else { return nil }
        var rest = payload
        rest.removeValue(forKey: "source")
        rest.removeValue(forKey: "script")
        guard !rest.isEmpty else { return nil }
        return JSONValue.object(rest).jsonText(pretty: pretty)
    }

    /// The invocation's payload as JSON (args for a non-code call).
    static func toolPayloadJSON(_ event: Event, pretty: Bool) -> String? {
        guard let payload = event.payload, !payload.isEmpty else { return nil }
        return JSONValue.object(payload).jsonText(pretty: pretty)
    }

    /// The terminal reply, read either from a `result` string or the payload
    /// object itself (the bus rule for object replies).
    static func toolResultText(_ event: Event) -> String {
        guard let payload = event.payload else { return "" }
        switch event.type {
        case "request.failed":
            if let error = payload["error"]?.string { return error }
            return payload["error"].map { $0.jsonText() } ?? ""
        case "request.rejected":
            return payload["reason"]?.string ?? ""
        default:
            break
        }
        guard let result = payload["result"] else {
            // No wrapper: the payload is the reply.
            var rest = payload
            rest.removeValue(forKey: "request_id")
            return rest.isEmpty ? "" : JSONValue.object(rest).jsonText(pretty: true)
        }
        if let text = result.string {
            // A JSON-in-a-string result is prettified; anything else is prose.
            if let data = text.data(using: .utf8),
               let value = try? JSONDecoder().decode(JSONValue.self, from: data),
               case .object = value {
                return value.jsonText(pretty: true)
            }
            return text
        }
        if case .array(let blocks) = result {
            // Multi-modal reply: summarize media instead of dumping base64.
            return blocks.map { block -> String in
                guard let object = block.object else { return block.jsonText() }
                switch object["type"]?.string ?? "" {
                case "text": return object["text"]?.string ?? ""
                case "image": return "[image \(object["mime"]?.string ?? "image")]"
                case "file": return "[file \(object["name"]?.string ?? object["path"]?.string ?? "")]"
                default: return block.jsonText()
                }
            }.joined(separator: "\n\n")
        }
        return result.jsonText(pretty: true)
    }

    /// Splits a `<system-reminder>…</system-reminder>` envelope from user prose,
    /// matching the WebUI's SystemReminderBlock split.
    static func splitSystemReminder(_ text: String) -> (reminder: String?, content: String) {
        guard let regex = try? NSRegularExpression(
            pattern: "<system-reminder>([\\s\\S]*?)</system-reminder>",
            options: []
        ) else { return (nil, text) }
        let range = NSRange(text.startIndex..., in: text)
        guard let match = regex.firstMatch(in: text, options: [], range: range) else {
            return (nil, text)
        }
        var reminder: String?
        if let inner = Range(match.range(at: 1), in: text) {
            let value = String(text[inner]).trimmingCharacters(in: .whitespacesAndNewlines)
            reminder = value.isEmpty ? nil : value
        }
        let content = regex.stringByReplacingMatches(in: text, options: [], range: range, withTemplate: "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return (reminder, content)
    }

    static func oneLine(_ text: String, _ limit: Int) -> String {
        let flat = text.split(whereSeparator: { $0.isWhitespace }).joined(separator: " ")
        return flat.count <= limit ? flat : String(flat.prefix(limit)) + "…"
    }
}
