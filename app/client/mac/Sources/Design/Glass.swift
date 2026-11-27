import SwiftUI

@available(macOS 26.0, *)
private func niqMakeGlass(tint: Color?, interactive: Bool) -> Glass {
    var glass: Glass = .regular
    if let tint { glass = glass.tint(tint) }
    if interactive { glass = glass.interactive(true) }
    return glass
}

extension View {
    /// Liquid Glass on macOS 26+, a standard material before it.
    @ViewBuilder
    func niqGlass<S: Shape>(_ shape: S, tint: Color? = nil, interactive: Bool = false) -> some View {
        if #available(macOS 26.0, *) {
            self.glassEffect(niqMakeGlass(tint: tint, interactive: interactive), in: shape)
        } else {
            self.background(.regularMaterial, in: shape)
        }
    }

    /// Glass button style on macOS 26+, bordered before it.
    @ViewBuilder
    func niqGlassButton() -> some View {
        if #available(macOS 26.0, *) {
            self.buttonStyle(.glass)
        } else {
            self.buttonStyle(.bordered)
        }
    }

    /// A subtle content-layer surface (HIG: glass is for controls/navigation,
    /// content uses standard materials). Square, matching the block language.
    func niqContentSurface(_ radius: CGFloat = 0) -> some View {
        self.background(.background.secondary, in: Rectangle())
    }
}
