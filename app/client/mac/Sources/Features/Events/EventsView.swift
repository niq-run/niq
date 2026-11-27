import SwiftUI

/// The raw event inspector.
///
/// WebUI parity: a dense one-line-per-event list (time · type · worker →
/// target · summary) with an inspector pane, plus the 调用链 filter that the
/// talk view's trace link drops you into.
struct EventsView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var filter = ""
        @Published var selectedID: String?
        @Published var typeFilter = ""
    }

    private var scoped: [Event] {
        var events = model.events
        if let trace = model.eventsFilterTrace, !trace.isEmpty {
            events = events.filter { $0.traceID == trace }
        }
        if !state.typeFilter.isEmpty {
            events = events.filter { $0.type == state.typeFilter }
        }
        let needle = state.filter.trimmingCharacters(in: .whitespaces).lowercased()
        if !needle.isEmpty {
            events = events.filter {
                $0.type.lowercased().contains(needle)
                    || ($0.workerID ?? "").lowercased().contains(needle)
                    || ($0.targetWorkerID ?? "").lowercased().contains(needle)
                    || $0.summary.lowercased().contains(needle)
            }
        }
        return events
    }

    private var selected: Event? {
        guard let id = state.selectedID else { return nil }
        return model.events.first { $0.id == id }
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            if model.eventsFilterTrace != nil { traceBanner }
            HSplitView {
                list
                    .frame(minWidth: 420, idealWidth: 560)
                inspector
                    .frame(minWidth: 320, idealWidth: 420)
            }
        }
    }

    // MARK: Header

    private var header: some View {
        PageHeader(title: "事件", count: scoped.count) {
            HStack(spacing: 8) {
                HStack(spacing: 6) {
                    Image(systemName: "magnifyingglass")
                        .font(.system(size: 10))
                        .foregroundStyle(Niq.textFaint)
                    TextField("按类型 / worker / 内容过滤", text: $state.filter)
                        .textFieldStyle(.plain)
                        .font(.system(size: Niq.caption))
                        .frame(width: 230)
                }
                .padding(.horizontal, 9)
                .padding(.vertical, 5)
                .background(Niq.field.opacity(0.5))
                .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))

                if !state.typeFilter.isEmpty {
                    Button {
                        state.typeFilter = ""
                    } label: {
                        HStack(spacing: 5) {
                            Text(state.typeFilter).font(Niq.mono(Niq.caption))
                            Image(systemName: "xmark").font(.system(size: 9))
                        }
                        .foregroundStyle(Niq.accent)
                        .padding(.horizontal, 9)
                        .padding(.vertical, 5)
                        .background(Niq.accentSoft)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private var traceBanner: some View {
        HStack(spacing: 8) {
            Image(systemName: "point.topleft.down.to.point.bottomright.curvepath")
                .font(.system(size: 10))
            Text("只在查看调用链")
                .font(.system(size: Niq.caption))
            if let trace = model.eventsFilterTrace {
                Text(trace).font(Niq.mono(Niq.micro)).lineLimit(1)
            }
            Spacer(minLength: 8)
            Button("清除") { model.eventsFilterTrace = nil }
                .buttonStyle(.plain)
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.accent)
        }
        .foregroundStyle(Niq.textDim)
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 7)
        .background(Niq.accentSoft.opacity(0.5))
    }

    // MARK: List

    private var list: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                columnHeader
                ForEach(scoped.reversed()) { event in
                    eventRow(event)
                }
            }
        }
        .background(Niq.window)
    }

    private var columnHeader: some View {
        HStack(spacing: 12) {
            Text("时间").frame(width: 66, alignment: .leading)
            Text("类型").frame(width: 168, alignment: .leading)
            Text("来源").frame(width: 112, alignment: .leading)
            Text("目标").frame(width: 112, alignment: .leading)
            Text("摘要")
            Spacer(minLength: 0)
        }
        .font(.system(size: Niq.caption, weight: .medium))
        .foregroundStyle(Niq.textFaint)
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 10)
        .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
    }

    private func eventRow(_ event: Event) -> some View {
        let selected = state.selectedID == event.id
        return HStack(spacing: 12) {
            Text(event.timeText)
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 66, alignment: .leading)

            Button {
                state.typeFilter = state.typeFilter == event.type ? "" : event.type
            } label: {
                Text(event.type)
                    .font(Niq.mono(Niq.caption))
                    .foregroundStyle(typeColor(event.type))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(typeColor(event.type).opacity(0.10),
                                in: Rectangle())
                    .frame(width: 168, alignment: .leading)
            }
            .buttonStyle(.plain)
            .help("只看这个类型")

            Text(event.workerID ?? "—")
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.accent)
                .lineLimit(1)
                .frame(width: 112, alignment: .leading)

            Text(event.targetWorkerID ?? (event.recipients?.joined(separator: ", ") ?? "—"))
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textDim)
                .lineLimit(1)
                .frame(width: 112, alignment: .leading)

            Text(summary(event))
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textDim)
                .lineLimit(1)

            Spacer(minLength: 0)
        }
        .padding(.horizontal, Niq.pagePadding)
        .padding(.vertical, 8)
        .contentShape(Rectangle())
        .background(selected ? Niq.accentSoft : Color.clear)
        .onTapGesture { state.selectedID = event.id }
    }

    private func summary(_ event: Event) -> String {
        if let name = event.payload?["name"]?.string, !name.isEmpty { return name }
        let text = event.summary
        if !text.isEmpty { return Transcript.oneLine(text, 120) }
        if let error = event.payload?["error"]?.string { return "error: " + Transcript.oneLine(error, 80) }
        return event.requestID.map { "request " + $0 } ?? ""
    }

    // MARK: Inspector

    @ViewBuilder
    private var inspector: some View {
        if let event = selected {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 8) {
                        Text(event.type)
                            .font(Niq.mono(13, weight: .semibold))
                            .foregroundStyle(typeColor(event.type))
                        Spacer(minLength: 8)
                        if let trace = event.traceID, !trace.isEmpty {
                            Button("调用链") { model.openTrace(trace) }
                                .buttonStyle(.plain)
                                .font(.system(size: 11))
                                .foregroundStyle(Niq.accent)
                        }
                    }

                    VStack(alignment: .leading, spacing: 5) {
                        field("id", event.id)
                        field("time", event.dateTimeText)
                        field("worker", event.workerID ?? "—")
                        field("target", event.targetWorkerID ?? "—")
                        if let recipients = event.recipients, !recipients.isEmpty {
                            field("recipients", recipients.joined(separator: ", "))
                        }
                        field("request", event.requestID ?? "—")
                        field("trace", event.traceID ?? "—")
                        if event.transient == true { field("transient", "true") }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("PAYLOAD")
                            .font(.system(size: 9.5, weight: .semibold))
                            .tracking(0.6)
                            .foregroundStyle(Niq.textFaint)
                        if let payload = event.payload {
                            CodeBlock(language: "json", code: JSONValue.object(payload).jsonText(pretty: true))
                        } else {
                            Text("（无 payload）").font(.system(size: 11.5)).foregroundStyle(Niq.textFaint)
                        }
                    }
                }
                .padding(24)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(Niq.window)
        } else {
            VStack(spacing: 8) {
                Image(systemName: "list.bullet.rectangle")
                    .font(.system(size: 22, weight: .light))
                    .foregroundStyle(Niq.textFaint)
                Text("选择一条事件查看 payload")
                    .font(.system(size: 12))
                    .foregroundStyle(Niq.textDim)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Niq.window)
        }
    }

    private func field(_ label: String, _ value: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Text(label)
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(width: 70, alignment: .leading)
            Text(value.isEmpty ? "—" : value)
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textDim)
                .textSelection(.enabled)
            Spacer(minLength: 0)
        }
    }

    private func typeColor(_ type: String) -> Color {
        if type.hasPrefix("reason.") { return Niq.reason }
        if type.hasSuffix(".failed") || type.hasSuffix(".rejected") || type.hasSuffix(".error") { return Niq.failed }
        if type.hasSuffix(".completed") || type.hasSuffix(".done") || type.hasSuffix(".succeeded") { return Niq.completed }
        if type.hasPrefix("request.") || type.hasPrefix("tool.") || type.hasSuffix(".call") || type.hasSuffix(".request") {
            return Niq.pending
        }
        return Niq.textDim
    }
}
