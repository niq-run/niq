import SwiftUI
import AppKit

/// Design tokens for the WebUI-aligned look.
///
/// The WebUI is flat and dense: a light grey shell, hairline borders, monospaced
/// identifiers, and a single blue accent (rgb 60,120,180) used for worker names,
/// chips and links. We keep those proportions and let the system handle
/// light/dark by leaning on semantic NSColors, then opt into Liquid Glass only
/// for the floating/control layer (composer, popovers, pills).
enum Niq {
    // MARK: Accent

    static let accent = Color(red: 60.0 / 255.0, green: 120.0 / 255.0, blue: 180.0 / 255.0)
    static let accentSoft = accent.opacity(0.10)
    static let accentBorder = accent.opacity(0.30)

    // MARK: Adaptive surfaces

    static var window: Color { Color(nsColor: .windowBackgroundColor) }
    static var sidebar: Color { Color(nsColor: .underPageBackgroundColor) }
    static var card: Color { Color(nsColor: .controlBackgroundColor) }
    static var field: Color { Color(nsColor: .textBackgroundColor) }
    static var border: Color { Color(nsColor: .separatorColor) }
    static var borderSoft: Color { Color(nsColor: .separatorColor).opacity(0.55) }
    static var text: Color { Color(nsColor: .labelColor) }
    static var textDim: Color { Color(nsColor: .secondaryLabelColor) }
    static var textFaint: Color { Color(nsColor: .tertiaryLabelColor) }

    // MARK: Outcome colours (event tags, tool status)

    static let completed = Color(red: 0.29, green: 0.55, blue: 0.34)
    static let failed = Color(red: 0.78, green: 0.20, blue: 0.18)
    static let pending = Color(red: 0.72, green: 0.52, blue: 0.13)
    static let reason = Color(red: 0.42, green: 0.36, blue: 0.72)
    static let neutral = Color(nsColor: .tertiaryLabelColor)

    // MARK: Type

    static func mono(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .monospaced)
    }
    static func sans(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight)
    }

    // MARK: Type scale
    //
    // One scale, shared by every surface. The WebUI's 12–14px body reads as
    // cramped on a desktop window, so the native client sits one step larger:
    // 15px prose, 12–13px metadata, 11px micro labels.
    static let display: CGFloat = 26   // big speaker names
    static let title: CGFloat = 17     // view headers
    static let body: CGFloat = 15      // message prose
    static let small: CGFloat = 13     // secondary rows, tool headers
    static let caption: CGFloat = 12   // metadata, chips
    static let micro: CGFloat = 11     // timestamps, faint labels

    static let radius: CGFloat = 2
    static let bubbleMax: CGFloat = 880
    /// The conversation's reading column: generous on a wide window, capped so
    /// prose never runs the full width of a 27" display.
    static let columnMax: CGFloat = 1060
    /// The gutter that keeps a message from spanning the column: short messages
    /// hug their content, long ones wrap inside the remaining width (the WebUI's
    /// 70% cap, expressed as a minimum gutter on the opposite side).
    static let bubbleGutter: CGFloat = 300
}

// MARK: - Layout constants

extension Niq {
    static let headerHeight: CGFloat = 60
    static let sidebarWidth: CGFloat = 286
    static let pagePadding: CGFloat = 28
}

// MARK: - Surfaces

extension View {
    /// A bordered content card (WebUI's message/tool block). Square by design:
    /// the WebUI's blocks are rectangles, not rounded cards.
    func niqCard(radius: CGFloat = Niq.radius, fill: Color = Niq.card) -> some View {
        self
            .background(fill, in: Rectangle())
            .overlay(
                Rectangle()
                    .strokeBorder(Niq.borderSoft, lineWidth: 1)
            )
    }

    /// A section header label in the sidebar (WebUI's bold small caps bands).
    func niqSectionLabel() -> some View {
        self
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(Niq.text)
            .textCase(nil)
    }

    /// Thin, adaptive hairline.
    func niqHairline(_ edge: Edge.Set = .bottom) -> some View {
        overlay(alignment: edge == .top ? .top : (edge == .bottom ? .bottom : .leading)) {
            Rectangle()
                .fill(Niq.borderSoft)
                .frame(height: edge == .leading || edge == .trailing ? nil : 1)
        }
    }
}

// MARK: - Small shared controls

/// Text that looks like a button but stays quiet until hovered.
struct QuietButtonStyle: ButtonStyle {
    var tint: Color = Niq.textDim
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 12))
            .foregroundStyle(configuration.isPressed ? Niq.accent : tint)
            .opacity(configuration.isPressed ? 0.7 : 1)
            .contentShape(Rectangle())
    }
}

/// A compact chip used for @targets, worker ids and status tokens.
struct Chip: View {
    let text: String
    var color: Color = Niq.accent
    var filled: Bool = false
    var body: some View {
        Text(text)
            .font(Niq.mono(Niq.caption))
            .foregroundStyle(filled ? Color.white : color)
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(filled ? color : color.opacity(0.12), in: Rectangle())
    }
}

/// A tiny inline status dot.
struct Dot: View {
    var color: Color
    var size: CGFloat = 7
    var body: some View {
        Circle().fill(color).frame(width: size, height: size)
    }
}

/// A monospaced identifier label.
struct Ident: View {
    let text: String
    var color: Color = Niq.textDim
    var size: CGFloat = Niq.caption
    var body: some View {
        Text(text).font(Niq.mono(size)).foregroundStyle(color)
    }
}

/// The bordered pill used by the secondary action row (附件 / 停止 / 模式).
struct OutlineButton: View {
    let title: String
    var tint: Color = Niq.textDim
    var enabled: Bool = true
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: Niq.small))
                .foregroundStyle(enabled ? tint : Niq.textFaint.opacity(0.6))
                .padding(.horizontal, 11)
                .padding(.vertical, 6)
                .overlay(
                    Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1)
                )
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
    }
}

/// The WebUI's `Pill`: a square outlined button used throughout the chrome.
struct Pill: View {
    let title: String
    var accent: Bool = false
    var enabled: Bool = true
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: Niq.small))
                .foregroundStyle(enabled ? (accent ? Niq.accent : Niq.textDim) : Niq.textFaint.opacity(0.55))
                .padding(.horizontal, 11)
                .padding(.vertical, 6)
                .overlay(
                    Rectangle().strokeBorder(accent ? Niq.accentBorder : Niq.borderSoft, lineWidth: 1)
                )
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
    }
}
