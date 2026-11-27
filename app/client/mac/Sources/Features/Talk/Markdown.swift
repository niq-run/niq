import SwiftUI
import AppKit

/// A deliberately small Markdown block model.
///
/// `AttributedString(markdown:)` handles inline emphasis and links, but drops
/// block structure (fences, tables). The conversation is dominated by those two
/// shapes, so we split them out first and render the prose with the system
/// parser.
enum MarkdownSegment: Hashable {
    case prose(String)
    case code(language: String?, body: String)
    case table(String)
}

enum MiniMarkdown {
    static func split(_ raw: String) -> [MarkdownSegment] {
        var out: [MarkdownSegment] = []
        var prose: [String] = []
        var table: [String] = []
        var code: [String] = []
        var codeLang: String? = nil
        var inCode = false

        func flushProse() {
            let text = prose.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            if !text.isEmpty { out.append(.prose(text)) }
            prose.removeAll()
        }
        func flushTable() {
            let text = table.joined(separator: "\n")
            if !table.isEmpty { out.append(.table(text)) }
            table.removeAll()
        }
        func flushCode() {
            let text = code.joined(separator: "\n")
            out.append(.code(language: codeLang, body: text))
            code.removeAll()
            codeLang = nil
        }

        for line in raw.components(separatedBy: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if inCode {
                if trimmed.hasPrefix("```") {
                    inCode = false
                    flushCode()
                } else {
                    code.append(line)
                }
                continue
            }
            if trimmed.hasPrefix("```") {
                flushProse()
                flushTable()
                inCode = true
                let lang = String(trimmed.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                codeLang = lang.isEmpty ? nil : lang
                continue
            }
            if trimmed.hasPrefix("|") {
                flushProse()
                table.append(line)
                continue
            }
            flushTable()
            prose.append(line)
        }
        if inCode { flushCode() }
        flushTable()
        flushProse()
        return out
    }
}

/// Rendered Markdown for message bodies.
struct MarkdownText: View {
    let raw: String
    var size: CGFloat = Niq.body
    var color: Color = Niq.text

    var body: some View {
        let segments = MiniMarkdown.split(raw)
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(segments.enumerated()), id: \.offset) { _, segment in
                switch segment {
                case .prose(let text):
                    prose(text)
                case .code(let language, let body):
                    CodeBlock(language: language, code: body)
                case .table(let text):
                    table(text)
                }
            }
        }
    }

    @ViewBuilder
    private func prose(_ text: String) -> some View {
        if let attributed = try? AttributedString(
            markdown: text,
            options: .init(interpretedSyntax: .full,
                           failurePolicy: .returnPartiallyParsedIfPossible)
        ) {
            Text(attributed)
                .font(.system(size: size))
                .foregroundStyle(color)
                .lineSpacing(4)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        } else {
            Text(text)
                .font(.system(size: size))
                .foregroundStyle(color)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func table(_ text: String) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Text(text)
                .font(Niq.mono(size - 1))
                .foregroundStyle(color)
                .textSelection(.enabled)
                .fixedSize(horizontal: true, vertical: true)
                .padding(12)
        }
        .background(Niq.sidebar.opacity(0.6))
        .overlay(
            Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1)
        )
    }
}

/// A fenced code block: language tag, wrap toggle and copy, body in monospace.
struct CodeBlock: View {
    let language: String?
    let code: String

    @StateObject private var box = CodeBlockBox()

    private final class CodeBlockBox: ObservableObject {
        @Published var wrapped = false
        @Published var copied = false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                if let language, !language.isEmpty {
                    Text(language)
                        .font(Niq.mono(11))
                        .foregroundStyle(Niq.textFaint)
                }
                Spacer(minLength: 8)
                Button { box.wrapped.toggle() } label: {
                    Text(box.wrapped ? "不换行" : "自动换行")
                        .font(.system(size: Niq.micro))
                        .foregroundStyle(Niq.textFaint)
                }
                .buttonStyle(.plain)
                Button { copy() } label: {
                    Text(box.copied ? "已复制" : "复制")
                        .font(.system(size: Niq.micro))
                        .foregroundStyle(box.copied ? Niq.completed : Niq.textFaint)
                }
                .buttonStyle(.plain)
            }
            .padding(.horizontal, 12)
            .padding(.top, 8)
            .padding(.bottom, 6)

            ScrollView(box.wrapped ? [] : [.horizontal], showsIndicators: false) {
                Text(code)
                    .font(Niq.mono(12.5))
                    .foregroundStyle(Niq.text)
                    .textSelection(.enabled)
                    .lineSpacing(3)
                    .fixedSize(horizontal: !box.wrapped, vertical: true)
                    .frame(maxWidth: box.wrapped ? .infinity : nil, alignment: .leading)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 11)
            }
        }
        .background(Niq.field.opacity(0.55))
        .overlay(
            Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1)
        )
    }

    private func copy() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(code, forType: .string)
        box.copied = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) { box.copied = false }
    }
}

func copyToPasteboard(_ text: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
}
