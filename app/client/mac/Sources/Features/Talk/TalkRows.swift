import SwiftUI
import AppKit

// MARK: - Shared bits

/// The big blue speaker name that opens a run (WebUI's row badge).
struct SpeakerHeader: View {
    let name: String
    var body: some View {
        Text(name)
            .font(Niq.mono(Niq.display, weight: .bold))
            .foregroundStyle(Niq.accent)
            .padding(.top, 10)
            .padding(.bottom, 2)
    }
}

/// The mode chip in a human message header.
func modeLabel(_ raw: String?) -> String? {
    switch raw ?? "" {
    case "append": return "追加模式"
    case "schedule": return "预约模式"
    case "interrupt": return "打断模式"
    default: return nil
    }
}

/// "-- 上下文窗口 262K · 已用 67% · 输入 176K · 输出 32" (right-aligned, dim).
/// When the round reported a breakdown, clicking opens the composition panel.
struct UsageLine: View {
    let usage: UsageInfo
    @StateObject private var box = Box()
    private final class Box: ObservableObject { @Published var open = false }

    private var canOpen: Bool { !usage.breakdown.isEmpty }

    var body: some View {
        HStack(spacing: 14) {
            if !usage.leftPart.isEmpty { Text(usage.leftPart) }
            if !usage.rightPart.isEmpty { Text(usage.rightPart) }
        }
        .font(Niq.mono(Niq.micro))
        .foregroundStyle(Niq.textFaint)
        .lineLimit(1)
        .truncationMode(.tail)
        .padding(.top, 4)
        .contentShape(Rectangle())
        .onTapGesture { if canOpen { box.open.toggle() } }
        .help(canOpen ? "本轮上下文用量，点击查看明细" : "本轮上下文用量（输入 · 输出 tokens）")
        .popover(isPresented: $box.open, arrowEdge: .bottom) {
            UsagePanel(usage: usage)
        }
    }
}

/// The context-usage composition panel (the WebUI's ContextUsageReport):
/// a stacked bar over the round's prompt plus a grouped legend.
struct UsagePanel: View {
    let usage: UsageInfo

    private struct Group: Identifiable {
        let id: String
        let label: String
        let color: Color
        let keys: [String]
        let children: [(label: String, keys: [String])]
    }

    private var groups: [Group] {
        let all: [Group] = [
            Group(id: "system", label: "系统提示词", color: Color(red: 0.43, green: 0.66, blue: 1.0),
                  keys: ["system"], children: []),
            Group(id: "program", label: "程序", color: Color(red: 0.65, green: 0.55, blue: 0.98),
                  keys: ["program.instructions", "program.playbooks"],
                  children: [("指令", ["program.instructions"]), ("剧本", ["program.playbooks"])]),
            Group(id: "tools", label: "工具定义", color: Color(red: 0.95, green: 0.71, blue: 0.36),
                  keys: ["tools.builtin", "tools.program", "tools.mcp", "tools.workers", "tools.other"],
                  children: [("内置", ["tools.builtin"]), ("程序 worker", ["tools.program"]),
                             ("MCP / 连接器", ["tools.mcp"]), ("其他 worker", ["tools.workers"])]),
            Group(id: "messages", label: "对话消息", color: Color(red: 0.32, green: 0.77, blue: 0.60),
                  keys: ["messages.user", "messages.assistant"],
                  children: [("用户与系统输入", ["messages.user"]), ("模型文本", ["messages.assistant"])]),
            Group(id: "toolCalls", label: "工具调用与结果", color: Color(red: 0.91, green: 0.47, blue: 0.62),
                  keys: ["messages.tool_calls", "messages.tool_results"],
                  children: [("调用参数", ["messages.tool_calls"]), ("调用结果", ["messages.tool_results"])]),
            Group(id: "thinking", label: "思考过程", color: Color(red: 0.49, green: 0.56, blue: 0.66),
                  keys: ["messages.thinking"], children: []),
            Group(id: "output", label: "本轮输出", color: Color(red: 0.75, green: 0.75, blue: 0.75),
                  keys: ["output"], children: []),
        ]
        return all.filter { group in
            usage.breakdown.contains { group.keys.contains($0.key) && $0.tokens > 0 }
        }
    }

    private var total: Int { usage.breakdown.reduce(0) { $0 + $1.tokens } }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Text("上下文用量")
                    .font(.system(size: Niq.small, weight: .semibold))
                Spacer(minLength: 12)
                if let pct = usage.contextPercent {
                    Text("已用 \(UsageInfo.percent(pct))%")
                        .font(Niq.mono(Niq.micro))
                        .foregroundStyle(Niq.textDim)
                }
            }

            bar

            VStack(alignment: .leading, spacing: 7) {
                ForEach(groups) { group in
                    let tokens = sum(group.keys)
                    HStack(spacing: 8) {
                        Circle().fill(group.color).frame(width: 8, height: 8)
                        Text(group.label)
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textDim)
                        Spacer(minLength: 16)
                        Text(UsageInfo.compact(tokens))
                            .font(Niq.mono(Niq.micro))
                            .foregroundStyle(Niq.textFaint)
                        Text(share(tokens))
                            .font(Niq.mono(Niq.micro))
                            .foregroundStyle(Niq.textFaint)
                            .frame(width: 40, alignment: .trailing)
                    }
                    ForEach(Array(group.children.enumerated()), id: \.offset) { _, child in
                        let childTokens = sum(child.keys)
                        if childTokens > 0 {
                            HStack(spacing: 8) {
                                Spacer().frame(width: 16)
                                Text(child.label)
                                    .font(.system(size: Niq.micro))
                                    .foregroundStyle(Niq.textFaint)
                                Spacer(minLength: 12)
                                Text(UsageInfo.compact(childTokens))
                                    .font(Niq.mono(Niq.micro))
                                    .foregroundStyle(Niq.textFaint)
                            }
                        }
                    }
                }
            }

            Divider().opacity(0.5)

            VStack(alignment: .leading, spacing: 4) {
                if let window = usage.contextWindow {
                    row("上下文窗口", UsageInfo.compact(window))
                }
                if let input = usage.inputTokens { row("输入", UsageInfo.compact(input)) }
                if let output = usage.outputTokens { row("输出", UsageInfo.compact(output)) }
                if usage.cacheReported, let read = usage.cacheRead {
                    let pct = usage.cacheHitPercent.map { UsageInfo.percent($0) } ?? "0"
                    row("缓存", "\(UsageInfo.compact(read)) (\(pct)%)")
                }
                if let model = usage.model, !model.isEmpty { row("模型", model) }
            }
        }
        .padding(16)
        .frame(width: 360)
    }

    private func sum(_ keys: [String]) -> Int {
        usage.breakdown.reduce(0) { acc, section in
            keys.contains(section.key) ? acc + section.tokens : acc
        }
    }

    private var bar: some View {
        GeometryReader { geo in
            HStack(spacing: 1) {
                ForEach(groups) { group in
                    let tokens = sum(group.keys)
                    let fraction = total > 0 ? CGFloat(tokens) / CGFloat(total) : 0
                    Rectangle()
                        .fill(group.color)
                        .frame(width: max(1, geo.size.width * fraction))
                }
            }
            .clipShape(Rectangle())
        }
        .frame(height: 8)
    }

    private func share(_ tokens: Int) -> String {
        guard total > 0 else { return "" }
        return "\(Int((Double(tokens) / Double(total) * 100).rounded()))%"
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack(spacing: 8) {
            Text(label)
                .font(.system(size: Niq.micro))
                .foregroundStyle(Niq.textFaint)
            Spacer(minLength: 12)
            Text(value)
                .font(Niq.mono(Niq.micro))
                .foregroundStyle(Niq.textDim)
        }
    }
}

/// A reusable "复制" affordance that reports its own success.
struct CopyControl: View {
    let text: String

    @StateObject private var box = Box()
    private final class Box: ObservableObject { @Published var copied = false }

    var body: some View {
        Button {
            copyToPasteboard(text)
            box.copied = true
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) { box.copied = false }
        } label: {
            Text(box.copied ? "已复制" : "复制")
                .font(.system(size: Niq.caption))
                .foregroundStyle(box.copied ? Niq.completed : Niq.textFaint)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help("复制文本")
    }
}

// MARK: - Row

/// One conversation row, dispatched by item kind.
struct TalkRowView: View {
    let item: TalkItem
    let showHeader: Bool
    @EnvironmentObject private var model: AppModel

    var body: some View {
        switch item {
        case .human(let human):
            side(right: human.alignRight) { HumanBubble(item: human, showHeader: showHeader) }
        case .assistant(let reply):
            side(right: false) { AssistantBlock(item: reply, showHeader: showHeader) }
        case .thinking(let thinking):
            side(right: false) { ThinkingCard(item: thinking, defaultOpen: model.expandThinking) }
        case .tool(let tool):
            side(right: false) { ToolCard(item: tool) }
        case .notice(let notice):
            // The human's own abort reads as an outgoing message (WebUI rule).
            let mine = notice.kind == .abort && notice.workerID == Transcript.humanID
            side(right: mine) { NoticeRowView(item: notice) }
        }
    }

    @ViewBuilder
    private func side<Content: View>(right: Bool, @ViewBuilder content: () -> Content) -> some View {
        // The gutter is a *minimum*: short messages hug their content, long ones
        // wrap inside the remaining width (the WebUI's 70% bubble cap).
        HStack(spacing: 0) {
            if right { Spacer(minLength: Niq.bubbleGutter) }
            content()
            if !right { Spacer(minLength: Niq.bubbleGutter) }
        }
    }
}

// MARK: - Human message

private struct HumanBubble: View {
    let item: HumanItem
    let showHeader: Bool
    @EnvironmentObject private var model: AppModel

    var body: some View {
        VStack(alignment: .trailing, spacing: 8) {
            if showHeader {
                SpeakerHeader(name: item.sender)
            }
            VStack(alignment: .leading, spacing: 10) {
                header
                if let reminder = item.reminder, !reminder.isEmpty {
                    reminderBlock(reminder)
                }
                if !item.text.isEmpty {
                    MarkdownText(raw: item.text, size: Niq.body)
                }
                if !item.attachments.isEmpty {
                    humanAttachments
                }
                if let trace = item.trace, !trace.isEmpty {
                    Button {
                        model.openTrace(trace)
                    } label: {
                        Text("调用链")
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                            .underline(pattern: .dot)
                    }
                    .buttonStyle(.plain)
                    .help("查看该调用链的全部事件")
                }
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
            .niqCard()
        }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Text(item.sender)
                .font(Niq.mono(Niq.caption, weight: .bold))
                .foregroundStyle(Niq.accent)
                .lineLimit(1)
            if item.targets.isEmpty {
                Text("广播")
                    .font(Niq.mono(Niq.caption, weight: .bold))
                    .foregroundStyle(Niq.accent)
            } else {
                ForEach(Array(item.targetNames.enumerated()), id: \.offset) { _, name in
                    Text("@\(name)")
                        .font(Niq.mono(Niq.caption, weight: .bold))
                        .foregroundStyle(Niq.accent)
                }
            }
            if let mode = modeLabel(item.mode) {
                Text(mode)
                    .font(.system(size: Niq.micro))
                    .foregroundStyle(Niq.textFaint)
            }
            Text(item.time)
                .font(Niq.mono(Niq.micro))
                .foregroundStyle(Niq.textFaint)
                .padding(.leading, 4)
        }
    }

    /// The input's `<system-reminder>` envelope, rendered apart from the prose.
    private func reminderBlock(_ reminder: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Rectangle().fill(Niq.border).frame(width: 2)
            VStack(alignment: .leading, spacing: 3) {
                Text("系统提示")
                    .font(.system(size: 10, weight: .semibold))
                    .tracking(0.4)
                    .foregroundStyle(Niq.textFaint)
                Text(reminder)
                    .font(.system(size: Niq.caption))
                    .italic()
                    .foregroundStyle(Niq.textDim)
                    .textSelection(.enabled)
                    .lineLimit(6)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 2)
    }

    private var humanAttachments: some View {
        HStack(spacing: 10) {
            ForEach(item.attachments) { attachment in
                if attachment.kind == .image,
                   let data = attachment.data,
                   let image = NSImage(data: Data(base64Encoded: data) ?? Data()) {
                    Image(nsImage: image)
                        .resizable()
                        .aspectRatio(contentMode: .fit)
                        .frame(maxHeight: 220)
                        .clipShape(Rectangle())
                } else {
                    HStack(spacing: 6) {
                        Image(systemName: "doc")
                            .font(.system(size: 11))
                        Text(attachment.name)
                            .lineLimit(1)
                    }
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textDim)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .overlay(
                        Rectangle()
                            .strokeBorder(Niq.borderSoft, lineWidth: 1)
                    )
                }
            }
        }
    }
}

// MARK: - Worker reply

private struct AssistantBlock: View {
    let item: AssistantItem
    let showHeader: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if showHeader {
                SpeakerHeader(name: item.name)
            }
            if let quote = item.quoteText, !quote.isEmpty {
                quoteBox(quote)
            }
            VStack(alignment: .leading, spacing: 12) {
                MarkdownText(raw: item.text, size: Niq.body)
                CopyControl(text: item.text)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
            .niqCard()

            if let usage = item.usage, usage.hasParts {
                UsageLine(usage: usage)
            }
        }
    }

    private func quoteBox(_ quote: String) -> some View {
        HStack(spacing: 8) {
            Rectangle()
                .fill(Niq.border)
                .frame(width: 2)
            if let worker = item.quoteWorker {
                Text("@\(worker)")
                    .font(Niq.mono(Niq.caption, weight: .bold))
                    .foregroundStyle(Niq.accent)
            }
            Text(Transcript.oneLine(quote, 220))
                .font(.system(size: Niq.caption))
                .italic()
                .foregroundStyle(Niq.textDim)
                .lineLimit(1)
        }
        .help(quote)
    }
}

// MARK: - Thinking

private struct ThinkingCard: View {
    let item: ThinkingItem
    let defaultOpen: Bool
    @StateObject private var box = Box()
    private final class Box: ObservableObject {
        @Published var open = false
        var configured = false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                box.open.toggle()
            } label: {
                HStack(spacing: 9) {
                    Dot(color: Niq.reason.opacity(0.55), size: 8)
                    Text("思考")
                        .font(.system(size: Niq.small))
                        .foregroundStyle(Niq.textDim)
                    Text("|").foregroundStyle(Niq.textFaint)
                    Text("\(item.text.count) 字")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                    Text(item.time)
                        .font(Niq.mono(Niq.micro))
                        .foregroundStyle(Niq.textFaint)
                        .padding(.leading, 6)
                    Image(systemName: box.open ? "chevron.down" : "chevron.right")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(Niq.textFaint)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if box.open {
                MarkdownText(raw: item.text, size: Niq.small, color: Niq.textDim)
                    .padding(.top, 10)
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .niqCard()
        .onAppear {
            // Apply the 展开思考 default once; a manual toggle then wins.
            if !box.configured {
                box.configured = true
                box.open = defaultOpen
            }
        }
    }
}

// MARK: - Tool call card

private struct ToolCard: View {
    let item: ToolItem
    @StateObject private var box = Box()
    private final class Box: ObservableObject { @Published var open = false }

    private var statusColor: Color {
        switch item.status {
        case .completed: return Niq.completed
        case .failed: return Niq.failed
        case .pending: return Niq.pending
        case .rejected: return Niq.neutral
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                box.open.toggle()
            } label: {
                HStack(spacing: 9) {
                    Dot(color: statusColor, size: 8)
                    Text("请求 [\(item.type)]")
                        .font(.system(size: Niq.small))
                        .foregroundStyle(Niq.textDim)
                        .lineLimit(1)
                    if item.chars > 0 {
                        Text("|").foregroundStyle(Niq.textFaint)
                        Text("\(item.chars) 字")
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                    }
                    if let direction = item.direction {
                        Text("|").foregroundStyle(Niq.textFaint)
                        Text(direction)
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                            .lineLimit(1)
                    }
                    Text(item.time)
                        .font(Niq.mono(Niq.micro))
                        .foregroundStyle(Niq.textFaint)
                        .padding(.leading, 6)
                    Image(systemName: box.open ? "chevron.down" : "chevron.right")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(Niq.textFaint)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if box.open {
                if let source = item.source {
                    section("代码") {
                        CodeBlock(language: "javascript", code: source)
                    }
                }
                if let args = item.args, !args.isEmpty {
                    section("参数") {
                        CodeBlock(language: "json", code: args)
                    }
                }
                if let result = item.result, !result.isEmpty {
                    section("请求结果") {
                        CodeBlock(language: "json", code: result)
                    }
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .niqCard(fill: box.open ? Niq.accentSoft : Niq.card)
    }

    @ViewBuilder
    private func section<Content: View>(_ label: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Divider().opacity(0.4)
            Text(label.uppercased())
                .font(.system(size: 10, weight: .semibold))
                .tracking(0.7)
                .foregroundStyle(Niq.textFaint)
            content()
        }
        .padding(.top, 10)
    }
}

// MARK: - Notice (abort / timeout / cancel / interrupted / approval)

private struct NoticeRowView: View {
    let item: NoticeItem

    private var color: Color {
        switch item.kind {
        case .abort: return Niq.failed
        case .timeout: return Niq.pending
        case .cancel, .interrupted: return Niq.neutral
        case .approval: return Niq.accent
        }
    }

    var body: some View {
        HStack(spacing: 9) {
            Dot(color: color, size: 7)
            HStack(spacing: 5) {
                Text(item.name)
                    .font(Niq.mono(Niq.caption, weight: .semibold))
                    .foregroundStyle(Niq.accent)
                Text(item.kind.label)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textDim)
            }
            Text("|").foregroundStyle(Niq.textFaint)
            Text(item.text)
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .lineLimit(1)
            Text(item.time)
                .font(Niq.mono(Niq.micro))
                .foregroundStyle(Niq.textFaint)
                .padding(.leading, 4)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 9)
        .niqCard()
    }
}

// MARK: - Folded process run

/// The 过程事件 chip: a full-width bar that expands in place.
struct ProcessGroupView: View {
    let run: RunBlock
    @StateObject private var box = Box()
    private final class Box: ObservableObject { @Published var open = false }

    private var detail: String {
        var parts: [String] = []
        if run.tools > 0 { parts.append("工具 \(run.tools)") }
        if run.thinking > 0 { parts.append("思考 \(run.thinking)") }
        if run.other > 0 { parts.append("其他 \(run.other)") }
        return parts.joined(separator: " · ")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if run.showHeader {
                SpeakerHeader(name: name(for: run.workerID))
            }
            Button {
                box.open.toggle()
            } label: {
                HStack(spacing: 9) {
                    Image(systemName: box.open ? "chevron.down" : "chevron.right")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(Niq.textDim)
                    Text("\(run.total) 条过程事件")
                        .font(.system(size: Niq.small))
                        .foregroundStyle(Niq.textDim)
                    Spacer(minLength: 10)
                    if !detail.isEmpty {
                        Text(detail)
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 11)
                .contentShape(Rectangle())
                .background(Niq.sidebar)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
                .overlay(alignment: .leading) {
                    Rectangle().fill(Niq.accent.opacity(0.45)).frame(width: 2)
                }
            }
            .buttonStyle(.plain)

            if box.open {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(run.rows) { row in
                        TalkRowView(item: row.item, showHeader: false)
                    }
                }
                .padding(.leading, 14)
                .overlay(alignment: .leading) {
                    Rectangle().fill(Niq.borderSoft).frame(width: 1)
                }
            }
        }
    }

    @EnvironmentObject private var model: AppModel
    private func name(for id: String) -> String { model.workerName(id) }
}
