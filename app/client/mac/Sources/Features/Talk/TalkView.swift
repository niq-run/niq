import SwiftUI

/// The conversation view.
///
/// Mirrors the WebUI's Talk page: a header with the watched workers, a scrolling
/// timeline of message/tool blocks (folding runs of process events), the live
/// streaming tail, and the composer.
struct TalkView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = TalkState()

    private final class TalkState: ObservableObject {
        @Published var follow = true
        var cacheKey = ""
        var items: [TalkItem] = []
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            timeline
            Composer()
        }
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: 12) {
            Text("对话")
                .font(.system(size: Niq.title, weight: .semibold))
            if !model.pendingApprovals.isEmpty {
                Button {
                    model.section = .approvals
                } label: {
                    HStack(spacing: 6) {
                        Dot(color: Niq.accent, size: 6)
                        Text("有 \(model.pendingApprovals.count) 条审批待处理")
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.text)
                    }
                    .padding(.horizontal, 9)
                    .padding(.vertical, 4)
                    .background(Niq.accentSoft, in: Rectangle())
                }
                .buttonStyle(.plain)
                .help("打开审批")
            }
            Spacer(minLength: 16)
            HStack(spacing: 7) {
                Text("正在监听")
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textFaint)
                if model.viewFilter.isEmpty {
                    Text("全部")
                        .font(.system(size: Niq.caption, weight: .medium))
                        .foregroundStyle(Niq.textDim)
                } else {
                    ForEach(model.viewFilter.sorted(), id: \.self) { id in
                        Chip(text: id)
                    }
                }
            }
            Button {
                state.follow.toggle()
            } label: {
                Image(systemName: state.follow ? "arrow.down.to.line.compact" : "pause.circle")
                    .font(.system(size: 13))
                    .foregroundStyle(state.follow ? Niq.accent : Niq.textFaint)
                    .padding(4)
            }
            .buttonStyle(.plain)
            .help(state.follow ? "正在跟随最新消息" : "已暂停跟随，点击恢复")
        }
        .padding(.horizontal, Niq.pagePadding)
        .frame(height: Niq.headerHeight)
        .background(Niq.window)
        .overlay(alignment: .bottom) {
            Rectangle().fill(Niq.borderSoft).frame(height: 1)
        }
    }

    // MARK: Timeline

    private var blocks: [TalkBlock] {
        let events = model.events
        let partners = Set(model.talkPartners.map(\.id))
        let key = "\(events.count)|\(events.last?.id ?? "")|\(model.viewFilter.sorted().joined(separator: ","))|\(partners.sorted().joined(separator: ","))|\(model.responseOnly)|\(model.workers.count)|\(model.workerNames.values.sorted().joined(separator: ","))"
        if state.cacheKey != key {
            state.cacheKey = key
            state.items = Transcript.build(events, options: .init(
                filter: model.viewFilter,
                partners: partners,
                responseOnly: model.responseOnly,
                names: model.workerNames
            ))
        }
        return Transcript.layout(state.items, collapse: model.collapseProcess)
    }

    private var timeline: some View {
        let list = blocks
        return ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    if model.hasEarlier && !list.isEmpty {
                        loadEarlierButton
                    }
                    if list.isEmpty && !hasStream {
                        emptyState
                    }
                    ForEach(list) { block in
                        blockView(block)
                            .id(block.id)
                    }
                    streamingTail
                }
                .padding(.horizontal, Niq.pagePadding)
                .padding(.top, 22)
                .padding(.bottom, 32)
                .frame(maxWidth: Niq.columnMax, alignment: .leading)
                .frame(maxWidth: .infinity, alignment: .center)
            }
            .onChange(of: model.events.count) { _, _ in scroll(proxy, list) }
            .onChange(of: model.streamingText) { _, _ in scroll(proxy, list) }
            .onChange(of: model.streamingThinking) { _, _ in scroll(proxy, list) }
        }
    }

    @ViewBuilder
    private func blockView(_ block: TalkBlock) -> some View {
        switch block {
        case .row(let row):
            TalkRowView(item: row.item, showHeader: row.showHeader)
        case .run(let run):
            ProcessGroupView(run: run)
        }
    }

    /// Pages one page of older history in (the WebUI's load-more, as a button).
    private var loadEarlierButton: some View {
        HStack {
            Spacer(minLength: 0)
            Button {
                Task { await model.loadEarlier() }
            } label: {
                HStack(spacing: 6) {
                    if model.loadingEarlier {
                        ProgressView().controlSize(.mini)
                    }
                    Text(model.loadingEarlier ? "载入中…" : "载入更早的消息")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textDim)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
            }
            .buttonStyle(.plain)
            .disabled(model.loadingEarlier)
            Spacer(minLength: 0)
        }
        .padding(.bottom, 4)
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "bubble.left.and.bubble.right")
                .font(.system(size: 30, weight: .light))
                .foregroundStyle(Niq.textFaint)
            Text(model.viewFilter.isEmpty ? "还没有消息" : "这些工作单元还没有消息")
                .font(.system(size: Niq.body))
                .foregroundStyle(Niq.textDim)
            Text("在下方输入，@ 一个或多个 worker")
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 100)
    }

    // MARK: Streaming

    private var hasStream: Bool {
        model.streamingMode && (!model.streamingText.isEmpty || !model.streamingThinking.isEmpty)
    }

    @ViewBuilder
    private var streamingTail: some View {
        if hasStream {
            VStack(alignment: .leading, spacing: 10) {
                if !model.streamingThinking.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(spacing: 7) {
                            Dot(color: Niq.reason.opacity(0.6), size: 7)
                            Text("思考中…")
                                .font(.system(size: Niq.caption))
                                .foregroundStyle(Niq.textFaint)
                        }
                        Text(model.streamingThinking)
                            .font(.system(size: Niq.small))
                            .foregroundStyle(Niq.textDim)
                            .textSelection(.enabled)
                    }
                    .padding(14)
                    .frame(maxWidth: Niq.bubbleMax, alignment: .leading)
                    .niqCard()
                }
                if !model.streamingText.isEmpty {
                    HStack(alignment: .bottom, spacing: 3) {
                        MarkdownText(raw: model.streamingText, size: Niq.body)
                        Text("▍").foregroundStyle(Niq.accent).font(.system(size: Niq.body))
                    }
                    .padding(.horizontal, 20)
                    .padding(.vertical, 16)
                    .frame(maxWidth: Niq.bubbleMax, alignment: .leading)
                    .niqCard()
                }
            }
            .id("stream-tail")
        }
    }

    private func scroll(_ proxy: ScrollViewProxy, _ list: [TalkBlock]) {
        guard state.follow else { return }
        if hasStream {
            withAnimation(.easeOut(duration: 0.15)) { proxy.scrollTo("stream-tail", anchor: .bottom) }
            return
        }
        if let last = list.last {
            withAnimation(.easeOut(duration: 0.15)) { proxy.scrollTo(last.id, anchor: .bottom) }
        }
    }
}
