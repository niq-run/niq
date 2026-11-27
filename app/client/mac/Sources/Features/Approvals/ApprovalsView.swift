import SwiftUI

/// Approval requests the project's approver tracks.
///
/// WebUI parity: pending first, then the decided history, each entry showing
/// what was asked (action / tool / path), who asked, when, and the verdict with
/// inline 批准 / 拒绝 actions while it is still pending.
struct ApprovalsView: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        VStack(spacing: 0) {
            header
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    if model.approvals.isEmpty {
                        empty
                    } else {
                        ForEach(model.approvals) { approval in
                            ApprovalCard(approval: approval)
                        }
                    }
                }
                .padding(Niq.pagePadding)
                .frame(maxWidth: 980, alignment: .leading)
                .frame(maxWidth: .infinity, alignment: .center)
            }
        }
        .background(Niq.window)
        .task { await model.loadApprovals() }
    }

    private var header: some View {
        PageHeader(title: "审批", count: nil) {
            HStack(spacing: 8) {
                if !model.pendingApprovals.isEmpty {
                    Text("\(model.pendingApprovals.count) 待处理")
                        .font(.system(size: Niq.caption, weight: .medium))
                        .foregroundStyle(Niq.accent)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 3)
                        .background(Niq.accentSoft)
                }
                Pill(title: "刷新") { Task { await model.loadApprovals() } }
            }
        }
    }

    private var empty: some View {
        VStack(spacing: 10) {
            Image(systemName: "checkmark.shield")
                .font(.system(size: 28, weight: .light))
                .foregroundStyle(Niq.textFaint)
            Text("没有待处理的审批")
                .font(.system(size: Niq.body))
                .foregroundStyle(Niq.textDim)
            Text("worker 请求扩大边界时会出现在这里。")
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 90)
    }
}

private struct ApprovalCard: View {
    let approval: ApprovalEntry
    @EnvironmentObject private var model: AppModel

    private var statusColor: Color {
        guard let approved = approval.decision?.approved else { return Niq.pending }
        return approved ? Niq.completed : Niq.failed
    }

    private var statusText: String {
        guard let approved = approval.decision?.approved else { return "待处理" }
        return approved ? "已批准" : "已拒绝"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 9) {
                Dot(color: statusColor, size: 8)
                Text(approval.title)
                    .font(.system(size: Niq.body, weight: .medium))
                    .foregroundStyle(Niq.text)
                Text(statusText)
                    .font(.system(size: Niq.micro))
                    .foregroundStyle(statusColor)
                Spacer(minLength: 12)
                Text(approval.timeText)
                    .font(Niq.mono(Niq.micro))
                    .foregroundStyle(Niq.textFaint)
            }

            HStack(spacing: 14) {
                meta("来源", approval.workerID ?? "—")
                if let tool = approval.tool, !tool.isEmpty { meta("工具", tool) }
                if let path = approval.path, !path.isEmpty { meta("路径", path) }
            }

            if let note = approval.decision?.note, !note.isEmpty {
                Text(note)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textDim)
                    .italic()
            }

            if let payload = approval.payload, !payload.isEmpty {
                DisclosureGroup {
                    CodeBlock(language: "json", code: JSONValue.object(payload).jsonText(pretty: true))
                        .padding(.top, 8)
                } label: {
                    Text("请求内容")
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textFaint)
                }
            }

            if approval.isPending {
                HStack(spacing: 8) {
                    Button {
                        Task { await model.decide(approval, approved: true) }
                    } label: {
                        Text("批准")
                            .font(.system(size: Niq.small, weight: .medium))
                            .foregroundStyle(.white)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 6)
                            .background(Niq.completed, in: Rectangle())
                    }
                    .buttonStyle(.plain)

                    Button {
                        Task { await model.decide(approval, approved: false) }
                    } label: {
                        Text("拒绝")
                            .font(.system(size: Niq.small, weight: .medium))
                            .foregroundStyle(Niq.failed)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 6)
                            .overlay(
                                Rectangle()
                                    .strokeBorder(Niq.failed.opacity(0.6), lineWidth: 1)
                            )
                    }
                    .buttonStyle(.plain)

                    Spacer(minLength: 8)

                    if let trace = approval.traceID, !trace.isEmpty {
                        Button("调用链") { model.openTrace(trace) }
                            .buttonStyle(.plain)
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.accent)
                    }
                }
            }
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .niqCard()
    }

    private func meta(_ label: String, _ value: String) -> some View {
        HStack(spacing: 6) {
            Text(label)
                .font(.system(size: Niq.micro))
                .foregroundStyle(Niq.textFaint)
            Text(value)
                .font(Niq.mono(Niq.caption))
                .foregroundStyle(Niq.textDim)
                .lineLimit(1)
        }
    }
}
