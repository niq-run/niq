import SwiftUI

/// App settings: the resolved gateway address and the located `niq` binary.
struct SettingsView: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        Form {
            LabeledContent("网关地址", value: model.gatewayURL.absoluteString)
            LabeledContent("网关状态", value: model.gatewayRunning ? "运行中" : "已停止")
            LabeledContent("niq 二进制", value: NiqCLI.binary() ?? "未找到")
        }
        .padding(20)
        .frame(width: 460)
    }
}
