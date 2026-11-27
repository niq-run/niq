import SwiftUI
import AppKit

// Shared chrome for the management surfaces. Square, thin-bordered, small-caps
// labels — the WebUI's language. Native controls (Toggle, Menu, sheet, popover)
// are used where the user expects them.

/// A titled block.
struct SectionBox<Content: View>: View {
    let title: String
    var subtitle: String? = nil
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(title)
                    .font(.system(size: Niq.caption, weight: .semibold))
                    .tracking(0.3)
                    .foregroundStyle(Niq.textDim)
                if let subtitle {
                    Text(subtitle)
                        .font(.system(size: Niq.micro))
                        .foregroundStyle(Niq.textFaint)
                }
                Spacer(minLength: 0)
            }
            content()
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .niqCard()
    }
}

/// A label + control row in a form.
struct FormRow<Content: View>: View {
    let label: String
    var width: CGFloat = 108
    @ViewBuilder var content: () -> Content

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(label)
                .font(.system(size: Niq.caption))
                .foregroundStyle(Niq.textFaint)
                .frame(width: width, alignment: .leading)
            content()
            Spacer(minLength: 0)
        }
    }
}

/// A styled multi-line editor (square, monospaced optional).
struct SquareTextEditor: View {
    @Binding var text: String
    var minHeight: CGFloat = 160
    var monospaced = false
    var placeholder: String = ""

    var body: some View {
        ZStack(alignment: .topLeading) {
            TextEditor(text: $text)
                .font(monospaced ? Niq.mono(Niq.caption) : .system(size: Niq.caption))
                .scrollContentBackground(.hidden)
                .padding(8)
                .frame(minHeight: minHeight)
                .background(Niq.field.opacity(0.5))
                .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))
            if text.isEmpty && !placeholder.isEmpty {
                Text(placeholder)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textFaint)
                    .padding(.horizontal, 13)
                    .padding(.vertical, 16)
                    .allowsHitTesting(false)
            }
        }
    }
}

/// A single-line input with the WebUI's box styling.
struct SquareField: View {
    @Binding var text: String
    var placeholder: String = ""
    var monospaced = false

    var body: some View {
        TextField(placeholder, text: $text)
            .textFieldStyle(.plain)
            .font(monospaced ? Niq.mono(Niq.caption) : .system(size: Niq.caption))
            .padding(.horizontal, 9)
            .padding(.vertical, 6)
            .background(Niq.field.opacity(0.5))
            .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))
    }
}

/// Square tab strip.
struct SquareTabs: View {
    let items: [String]
    @Binding var selection: Int

    var body: some View {
        HStack(spacing: 0) {
            ForEach(Array(items.enumerated()), id: \.offset) { index, title in
                Button {
                    selection = index
                } label: {
                    Text(title)
                        .font(.system(size: Niq.caption, weight: selection == index ? .semibold : .regular))
                        .foregroundStyle(selection == index ? Niq.accent : Niq.textDim)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 7)
                        .background(selection == index ? Niq.accentSoft : Color.clear)
                        .overlay(alignment: .bottom) {
                            Rectangle()
                                .fill(selection == index ? Niq.accent : Color.clear)
                                .frame(height: 2)
                        }
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            Spacer(minLength: 0)
        }
        .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
    }
}

/// A page header with the WebUI's title + count + actions shape.
struct PageHeader<Trailing: View>: View {
    let title: String
    var count: Int? = nil
    var subtitle: String? = nil
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(spacing: 10) {
            Text(title)
                .font(.system(size: Niq.title, weight: .semibold))
            if let count {
                Text("(\(count))")
                    .font(Niq.mono(Niq.caption))
                    .foregroundStyle(Niq.textFaint)
            }
            if let subtitle {
                Text(subtitle)
                    .font(.system(size: Niq.caption))
                    .foregroundStyle(Niq.textFaint)
            }
            Spacer(minLength: 14)
            trailing()
        }
        .padding(.horizontal, Niq.pagePadding)
        .frame(height: Niq.headerHeight)
        .overlay(alignment: .bottom) { Rectangle().fill(Niq.borderSoft).frame(height: 1) }
    }
}

/// Decode a JSON text into a value, or nil with a message.
func parseJSON(_ text: String) -> (JSONValue?, String?) {
    guard let data = text.data(using: .utf8) else { return (nil, "编码错误") }
    do {
        let value = try JSONDecoder().decode(JSONValue.self, from: data)
        return (value, nil)
    } catch {
        return (nil, "JSON 解析失败：\(error.localizedDescription)")
    }
}

/// A confirmation dialog driven by plain buttons (native `.confirmationDialog`
/// is used at the call sites where the action is destructive).
func confirmDestructive(_ title: String, _ message: String, action: @escaping () -> Void) {
    let alert = NSAlert()
    alert.messageText = title
    alert.informativeText = message
    alert.alertStyle = .warning
    alert.addButton(withTitle: "确定")
    alert.addButton(withTitle: "取消")
    if alert.runModal() == .alertFirstButtonReturn { action() }
}
