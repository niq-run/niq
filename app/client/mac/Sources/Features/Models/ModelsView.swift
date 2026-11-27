import SwiftUI
import AppKit

/// The model-provider catalog (control plane `provider.json`).
///
/// WebUI parity: a per-provider card view plus a JSON editor; saving PUTs the
/// whole document back. The visual half is read-only by design — the JSON editor
/// is the write path (the WebUI's two tabs, minus the per-field form).
struct ModelsView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var state = State()

    private final class State: ObservableObject {
        @Published var tab = 0
        @Published var json = ""
        @Published var note = ""
        @Published var loaded = false
    }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "模型供应商", subtitle: "provider.json") {
                HStack(spacing: 8) {
                    HStack(spacing: 0) {
                        tabButton("可视化", 0)
                        tabButton("JSON", 1)
                    }
                    .overlay(Rectangle().strokeBorder(Niq.borderSoft, lineWidth: 1))

                    if state.tab == 1 {
                        if !state.note.isEmpty {
                            Text(state.note)
                                .font(.system(size: Niq.caption))
                                .foregroundStyle(state.note == "已保存" ? Niq.completed : Niq.failed)
                        }
                        Pill(title: "保存", accent: true) { save() }
                    }
                    Pill(title: "刷新") { Task { await reload() } }
                }
            }

            if state.tab == 0 {
                visual
            } else {
                SquareTextEditor(text: $state.json, minHeight: 520, monospaced: true, placeholder: "{ … }")
                    .padding(Niq.pagePadding)
            }
        }
        .background(Niq.window)
        .task { await reload() }
        .onChange(of: state.tab) { _, tab in
            if tab == 0 { Task { await model.loadProviders() } }
        }
    }

    private func tabButton(_ title: String, _ index: Int) -> some View {
        Button {
            state.tab = index
        } label: {
            Text(title)
                .font(.system(size: Niq.caption, weight: state.tab == index ? .semibold : .regular))
                .foregroundStyle(state.tab == index ? Niq.accent : Niq.textDim)
                .padding(.horizontal, 10)
                .padding(.vertical, 5)
                .background(state.tab == index ? Niq.accentSoft : Color.clear)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    // MARK: Visual

    private var visual: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if let config = model.providers, !(config.providers ?? []).isEmpty {
                    ForEach(config.providers ?? []) { provider in
                        providerCard(provider, active: config.active == provider.name)
                    }
                } else {
                    empty
                }
            }
            .padding(Niq.pagePadding)
            .frame(maxWidth: 980, alignment: .leading)
            .frame(maxWidth: .infinity, alignment: .center)
        }
    }

    private func providerCard(_ provider: ProviderInfo, active: Bool) -> some View {
        SectionBox(title: provider.name, subtitle: provider.base_url) {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    if active {
                        Chip(text: "默认", color: Niq.accent)
                    }
                    if let type = provider.type {
                        Chip(text: type, color: Niq.textDim)
                    }
                    if let model = provider.model {
                        Text("默认模型: \(model)")
                            .font(Niq.mono(Niq.micro))
                            .foregroundStyle(Niq.textFaint)
                    }
                    Spacer(minLength: 0)
                }
                if !provider.modelNames.isEmpty {
                    let columns = [GridItem(.adaptive(minimum: 170), spacing: 8)]
                    LazyVGrid(columns: columns, alignment: .leading, spacing: 8) {
                        ForEach(provider.modelNames, id: \.self) { name in
                            Text(name)
                                .font(Niq.mono(Niq.caption))
                                .foregroundStyle(Niq.textDim)
                                .lineLimit(1)
                                .padding(.horizontal, 9)
                                .padding(.vertical, 5)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Niq.sidebar.opacity(0.6))
                        }
                    }
                }
            }
        }
    }

    private var empty: some View {
        VStack(spacing: 8) {
            Image(systemName: "cpu")
                .font(.system(size: 22, weight: .light))
                .foregroundStyle(Niq.textFaint)
            Text("没有模型配置")
                .font(.system(size: 12))
                .foregroundStyle(Niq.textDim)
            Text("切到「JSON」标签直接编辑 provider.json 后保存。")
                .font(.system(size: 11))
                .foregroundStyle(Niq.textFaint)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 60)
    }

    // MARK: Actions

    private func reload() async {
        await model.loadProviders()
        await model.loadProvidersDoc()
        if let document = model.providersDoc {
            state.json = document.jsonText(pretty: true)
            state.loaded = true
        }
    }

    private func save() {
        let (document, error) = parseJSON(state.json)
        guard let document else {
            state.note = error ?? "JSON 无效"
            return
        }
        Task {
            let ok = await model.saveProviders(document)
            state.note = ok ? "已保存" : "保存失败"
        }
    }
}
