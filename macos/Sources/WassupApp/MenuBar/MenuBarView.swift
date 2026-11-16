import SwiftUI
import AppKit

struct MenuBarLabel: View {
    let count: Int
    private var icon: NSImage? {
        guard let url = Bundle.main.url(forResource: "MenuBarIcon", withExtension: "png"), let image = NSImage(contentsOf: url) else { return nil }
        if let retinaURL = Bundle.main.url(forResource: "MenuBarIcon@2x", withExtension: "png"),
           let data = try? Data(contentsOf: retinaURL), let retina = NSBitmapImageRep(data: data) {
            retina.size = NSSize(width: 18, height: 18)
            image.addRepresentation(retina)
        }
        image.size = NSSize(width: 18, height: 18)
        image.isTemplate = true
        return image
    }
    var body: some View {
        HStack(spacing: 4) {
            if let icon { Image(nsImage: icon) } else { Image(systemName: "bubble.left.and.bubble.right") }
            if count > 0 { Text(String(count)).font(.system(size: 11, weight: .semibold)).monospacedDigit() }
        }.accessibilityLabel(count > 0 ? "wassup, \(count) waiting for approval" : "wassup")
    }
}
struct MenuBarView: View {
    @Bindable var model: AppModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.renderingSnapshot) private var snapshot
    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                WassupLogo().frame(width: 39, height: 39)
                VStack(alignment: .leading, spacing: 4) {
                    Text(model.status.displayName).font(.system(size: 14, weight: .semibold)).lineLimit(1)
                    HStack(spacing: 5) {
                        Circle().fill(model.status.connected ? Palette.accent : model.status.loggedIn ? Palette.secondary : Palette.tertiary).frame(width: 5, height: 5)
                        Text(model.status.connected ? "Connected" : model.status.loggedIn ? "Connecting…" : "Not linked").font(.system(size: 10))
                        if !model.status.phone.isEmpty { Text("·  \(model.status.phone)").font(.system(size: 10)).lineLimit(1) }
                    }.foregroundStyle(Palette.secondary)
                }
                Spacer(minLength: 0)
            }.padding(18)
            Rectangle().fill(.white.opacity(0.08)).frame(height: 1)
            HStack {
                Text("Waiting for you").font(.system(size: 12, weight: .semibold))
                Spacer()
                if !model.drafts.isEmpty { Text(String(model.drafts.count)).font(.system(size: 10, weight: .semibold)).foregroundStyle(Palette.secondary).frame(minWidth: 20, minHeight: 18).background(.white.opacity(0.04), in: Capsule()) }
            }.padding(.horizontal, 18).padding(.top, 18).padding(.bottom, 12)
            if let error = model.refreshError { ErrorBanner(error: error).padding(.horizontal, 14).padding(.bottom, 10) }
            if model.drafts.isEmpty {
                VStack(spacing: 12) {
                    ZStack {
                        Circle().fill(.white.opacity(0.04)).frame(width: 56, height: 56)
                        Image(systemName: "checkmark.bubble").font(.system(size: 25, weight: .light)).foregroundStyle(Palette.accent)
                    }
                    Text("Nothing waiting.").font(.system(size: 14, weight: .medium))
                    Text("When your AI wants to send something,\nit shows up here.").font(.system(size: 12)).foregroundStyle(Palette.secondary).multilineTextAlignment(.center).lineSpacing(3)
                }.frame(maxWidth: .infinity).padding(.vertical, 28).padding(.bottom, 13)
            } else {
                if snapshot {
                    draftList
                } else {
                    ScrollView { draftList }.scrollIndicators(.hidden).frame(maxHeight: 420)
                }
            }
            Rectangle().fill(.white.opacity(0.08)).frame(height: 1)
            VStack(spacing: 0) {
                footer("Set up…", symbol: "slider.horizontal.3") { model.openSetup?() }
                footer("Install command line tool…", symbol: "terminal") { model.installCommandLineTool() }
                footer("Quit wassup", symbol: "power") { NSApp.terminate(nil) }
            }.padding(8)
        }.frame(width: 340).fixedSize(horizontal: false, vertical: true)
            .background(AppBackground()).preferredColorScheme(.dark).foregroundStyle(Palette.text)
            .animation(reduceMotion ? nil : .spring(response: 0.45, dampingFraction: 0.85), value: model.drafts.map(\.id))
    }
    private var draftList: some View {
        VStack(spacing: 10) { ForEach(model.drafts) { draft in DraftCard(draft: draft, model: model) } }
            .padding(.horizontal, 14).padding(.bottom, 16)
    }
    private func footer(_ title: String, symbol: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 10) { Image(systemName: symbol).frame(width: 15); Text(title); Spacer() }.font(.system(size: 12)).foregroundStyle(Palette.secondary).padding(.horizontal, 10).frame(height: 31).contentShape(Rectangle())
        }.buttonStyle(FooterButtonStyle())
    }
}
struct FooterButtonStyle: ButtonStyle {
    @State private var hover = false
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.background(.white.opacity(hover || configuration.isPressed ? 0.06 : 0), in: RoundedRectangle(cornerRadius: 7)).onHover { hover = $0 }
    }
}
struct DraftCard: View {
    let draft: Draft
    @Bindable var model: AppModel
    @Environment(\.renderingSnapshot) private var snapshot
    var body: some View {
        VStack(alignment: .leading, spacing: 11) {
            HStack(alignment: .top, spacing: 6) {
                Text(draft.summary).font(.system(size: 13, weight: .semibold)).fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
                Text("#\(draft.id)").font(.system(size: 10, design: .monospaced)).foregroundStyle(Palette.tertiary)
            }
            if snapshot {
                quote
            } else {
                quote.textSelection(.enabled)
            }
            TimelineView(.periodic(from: .now, by: 1)) { context in
                HStack(spacing: 5) {
                    Image(systemName: "clock").font(.system(size: 9))
                    Text(draft.expiry(at: context.date)).font(.system(size: 10))
                    Spacer()
                    if !draft.source.isEmpty { Text(draft.source == "mcp" ? "AI app" : draft.source.capitalized).font(.system(size: 10)) }
                }.foregroundStyle(Palette.secondary)
            }
            HStack(spacing: 8) {
                Button { Task { await model.resolve(draft.id, approve: true) } } label: {
                    HStack(spacing: 6) {
                        if model.resolved.contains(draft.id) { Image(systemName: "checkmark") }
                        else if model.resolving.contains(draft.id) { ProgressView().controlSize(.mini).tint(Palette.background) }
                        Text(model.resolved.contains(draft.id) ? "Done" : "Approve")
                    }.frame(minWidth: 65)
                }.buttonStyle(CapsuleButtonStyle(compact: true))
                Button("Decline") { Task { await model.resolve(draft.id, approve: false) } }.buttonStyle(CapsuleButtonStyle(primary: false, compact: true))
            }.disabled(model.resolving.contains(draft.id) || model.resolved.contains(draft.id) || (draft.expires.map { $0 <= Date() } ?? false))
        }.padding(15).glassPanel(radius: 15)
    }
    private var quote: some View {
        Text("“\(draft.preview)”").font(.system(size: 12)).foregroundStyle(Palette.text).lineSpacing(3).fixedSize(horizontal: false, vertical: true)
    }
}
