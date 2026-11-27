import SwiftUI
import AppKit
import UniformTypeIdentifiers

/// The message composer: recipient chips, attachments, mode, stop, send.
///
/// WebUI layout: the text field sits on top, the action row below with the
/// recipient chip on the left and 附件 / 停止 / 模式 / 发送 on the right. The
/// whole panel is capped to the conversation's reading column so it lines up
/// with the timeline above it.
struct Composer: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var box = ComposerBox()

    private final class ComposerBox: ObservableObject {
        @Published var text = ""
        @Published var expanded = false
        @Published var noTargetFlash = false
        @Published var attachments: [StagedAttachment] = []
        @Published var uploading = 0
        @Published var note = ""
    }

    /// The WebUI's limits: at most 4 attachments, images inline up to 5 MB.
    private let maxAttachments = 4
    private let maxImageBytes = 5 * 1024 * 1024

    private var trimmed: String { box.text.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var canSend: Bool {
        (!trimmed.isEmpty || !box.attachments.isEmpty)
            && !model.sendTargets.isEmpty
            && model.selectedProjectID != nil
            && box.uploading == 0
    }
    private var recipients: [Worker] { model.talkPartners.filter { model.sendTargets.contains($0.id) } }

    var body: some View {
        VStack(spacing: 10) {
            topBar
            if !box.attachments.isEmpty || box.uploading > 0 || !box.note.isEmpty {
                attachmentStrip
            }
            editor
            actionRow
        }
        .padding(.horizontal, 18)
        .padding(.top, 8)
        .padding(.bottom, 18)
        .frame(maxWidth: Niq.columnMax)
        .frame(maxWidth: .infinity)
        .background(alignment: .top) {
            Rectangle().fill(Niq.borderSoft).frame(height: 1)
        }
        .onDrop(of: [.fileURL], isTargeted: nil) { providers in
            for provider in providers {
                _ = provider.loadObject(ofClass: URL.self) { url, _ in
                    guard let url else { return }
                    DispatchQueue.main.async { stage(url) }
                }
            }
            return true
        }
    }

    // MARK: Top bar (resize affordance)

    private var topBar: some View {
        HStack(spacing: 0) {
            Spacer()
            Button {
                box.expanded.toggle()
            } label: {
                Image(systemName: box.expanded ? "chevron.down" : "chevron.up")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(Niq.textFaint)
                    .frame(width: 30, height: 20)
                    .overlay(
                        Rectangle()
                            .strokeBorder(Niq.borderSoft, lineWidth: 1)
                    )
            }
            .buttonStyle(.plain)
            .help(box.expanded ? "收起输入框" : "展开输入框")
        }
        .frame(height: 20)
    }

    // MARK: Attachments

    private var attachmentStrip: some View {
        HStack(spacing: 8) {
            ForEach(box.attachments) { attachment in
                HStack(spacing: 6) {
                    if attachment.kind == .image,
                       let data = attachment.data,
                       let image = NSImage(data: Data(base64Encoded: data) ?? Data()) {
                        Image(nsImage: image)
                            .resizable()
                            .aspectRatio(contentMode: .fill)
                            .frame(width: 26, height: 26)
                            .clipShape(Rectangle())
                    } else {
                        Image(systemName: "doc")
                            .font(.system(size: 11))
                            .foregroundStyle(Niq.textDim)
                    }
                    Text(attachment.name)
                        .font(.system(size: Niq.caption))
                        .foregroundStyle(Niq.textDim)
                        .lineLimit(1)
                    Button {
                        box.attachments.removeAll { $0.id == attachment.id }
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 8, weight: .semibold))
                            .foregroundStyle(Niq.textFaint)
                    }
                    .buttonStyle(.plain)
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
            }
            if box.uploading > 0 {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.mini)
                    Text("上传中…")
                        .font(.system(size: Niq.micro))
                        .foregroundStyle(Niq.textFaint)
                }
            }
            if !box.note.isEmpty {
                Text(box.note)
                    .font(.system(size: Niq.micro))
                    .foregroundStyle(Niq.failed)
                    .lineLimit(1)
            }
            Spacer(minLength: 0)
        }
    }

    // MARK: Editor

    private var editor: some View {
        TextField("输入消息…（@ 一个或多个 worker，Shift+Enter 换行）",
                  text: $box.text, axis: .vertical)
            .textFieldStyle(.plain)
            .font(.system(size: Niq.body))
            .lineSpacing(4)
            .lineLimit(box.expanded ? 16 : 3, reservesSpace: false)
            .frame(minHeight: box.expanded ? 220 : 76, alignment: .topLeading)
            .padding(.horizontal, 16)
            .padding(.vertical, 13)
            .background(Niq.field.opacity(0.45))
            .overlay(
                Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1)
            )
            .onSubmit(send)
            .disabled(model.selectedProjectID == nil)
    }

    // MARK: Action row

    private var actionRow: some View {
        HStack(spacing: 10) {
            targetChip

            if box.noTargetFlash && model.sendTargets.isEmpty {
                Text("先选一个 @对象")
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.pending)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .overlay(
                        Rectangle()
                            .strokeBorder(Niq.pending.opacity(0.7), lineWidth: 1)
                    )
            }

            Spacer(minLength: 10)

            OutlineButton(title: "附件") { pickFiles() }
                .help("添加图片或文件")

            OutlineButton(title: "停止", enabled: !model.sendTargets.isEmpty) {
                Task { await model.abort() }
            }
            .help("请求 worker 停下当前工作")

            modeMenu
            sendButton
        }
    }

    // MARK: Recipients

    private var targetChip: some View {
        Menu {
            if model.talkPartners.isEmpty {
                Text("没有可对话的工作单元")
            } else {
                ForEach(model.talkPartners) { worker in
                    Button {
                        model.toggleSendTarget(worker.id)
                    } label: {
                        Label("\(worker.displayName)\(model.sendTargets.contains(worker.id) ? "  ✓" : "")",
                              systemImage: model.sendTargets.contains(worker.id) ? "checkmark.circle.fill" : "circle")
                    }
                }
                Divider()
                Button("清空") { model.sendTargets = [] }
            }
        } label: {
            HStack(spacing: 7) {
                if recipients.isEmpty {
                    Text("选择对象")
                        .font(.system(size: Niq.small))
                        .foregroundStyle(Niq.textFaint)
                } else {
                    ForEach(recipients.prefix(3)) { worker in
                        Text("@\(worker.displayName)")
                            .font(Niq.mono(Niq.caption))
                            .foregroundStyle(Niq.accent)
                            .padding(.horizontal, 8)
                            .padding(.vertical, 2)
                            .background(Niq.accentSoft, in: Rectangle())
                    }
                    if recipients.count > 3 {
                        Text("+\(recipients.count - 3)")
                            .font(.system(size: Niq.caption))
                            .foregroundStyle(Niq.textFaint)
                    }
                }
                Image(systemName: "chevron.up")
                    .font(.system(size: 9, weight: .semibold))
                    .foregroundStyle(Niq.textFaint)
            }
            .padding(.horizontal, 11)
            .padding(.vertical, 7)
            .overlay(
                Rectangle()
                    .strokeBorder(recipients.isEmpty ? Niq.borderSoft : Niq.accentBorder, lineWidth: 1)
            )
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .help("选择这条消息的接收者")
    }

    // MARK: Mode

    private var modeMenu: some View {
        Menu {
            ForEach(TalkInputMode.allCases) { mode in
                Button {
                    model.inputMode = mode
                } label: {
                    Text(mode.label + (model.inputMode == mode ? "  ✓" : ""))
                }
            }
        } label: {
            Text("模式: \(model.inputMode.label)")
                .font(.system(size: Niq.small))
                .foregroundStyle(Niq.textDim)
                .padding(.horizontal, 12)
                .padding(.vertical, 7)
                .overlay(
                    Rectangle()
                        .strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .help(model.inputMode.hint)
    }

    // MARK: Send

    private var sendButton: some View {
        Button(action: send) {
            Text("发送")
                .font(.system(size: Niq.small, weight: .medium))
                .foregroundStyle(canSend ? Color.white : Niq.textFaint)
                .padding(.horizontal, 22)
                .padding(.vertical, 7)
                .background(
                    Rectangle()
                        .fill(canSend ? Niq.accent : Niq.borderSoft.opacity(0.5))
                )
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(canSend ? "发送" : "需要接收者和内容")
    }

    // MARK: Actions

    private func send() {
        guard !model.sendTargets.isEmpty else {
            box.noTargetFlash = true
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) { box.noTargetFlash = false }
            return
        }
        guard canSend else { return }
        let text = box.text
        let attachments = box.attachments
        box.text = ""
        box.attachments = []
        box.note = ""
        Task { await model.send(text: text, attachments: attachments) }
    }

    /// Pick files: images ride inline (base64), everything else uploads first.
    private func pickFiles() {
        guard model.selectedProjectID != nil else { return }
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = true
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.prompt = "添加"
        guard panel.runModal() == .OK else { return }
        for url in panel.urls {
            guard box.attachments.count + box.uploading < maxAttachments else {
                box.note = "最多 \(maxAttachments) 个附件"
                break
            }
            stage(url)
        }
    }

    private func stage(_ url: URL) {
        let name = url.lastPathComponent
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType
        let isImage = UTType(filenameExtension: url.pathExtension)?.conforms(to: .image) ?? false
        let size = (try? FileManager.default.attributesOfItem(atPath: url.path)[.size] as? Int64) ?? 0

        if isImage {
            guard let data = try? Data(contentsOf: url) else {
                box.note = "无法读取 \(name)"
                return
            }
            guard data.count <= maxImageBytes else {
                box.note = "\(name) 超过 5MB"
                return
            }
            box.attachments.append(StagedAttachment(
                id: UUID().uuidString, kind: .image, name: name, mime: mime,
                data: data.base64EncodedString(), path: nil, size: size
            ))
            return
        }

        box.uploading += 1
        Task {
            let uploaded = await model.uploadAttachment(url)
            box.uploading -= 1
            if let uploaded {
                box.attachments.append(StagedAttachment(
                    id: UUID().uuidString, kind: .file, name: uploaded.name ?? name, mime: mime,
                    data: nil, path: uploaded.path, size: uploaded.size ?? size
                ))
            } else {
                box.note = "上传失败：\(name)"
            }
        }
    }
}
