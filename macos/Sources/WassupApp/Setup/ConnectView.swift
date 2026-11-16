import SwiftUI
import AppKit

struct ConnectView: View {
    @Bindable var model: AppModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        VStack(spacing: 19) {
            StepHeading(title: "Meet your AI apps.", subtitle: "Pick the apps you'd like to use with WhatsApp.")
            VStack(spacing: 0) {
                ForEach(model.apps) { app in
                    HStack(spacing: 13) {
                        AppIcon(app: app).frame(width: 32, height: 32)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(app.name).font(.system(size: 14, weight: .medium))
                            if !app.found { Text("Not installed").font(.system(size: 11)).foregroundStyle(Palette.secondary) }
                            else if app.id == "claude-desktop" && app.connected { Text("Quit and reopen Claude to finish").font(.system(size: 10)).foregroundStyle(Palette.secondary) }
                        }
                        Spacer()
                        if app.found {
                            Button { if !app.connected { model.connect(app.id) } } label: {
                                HStack(spacing: 6) {
                                    if app.connecting { ProgressView().controlSize(.mini) }
                                    else if app.connected { Image(systemName: "checkmark").foregroundStyle(Palette.accent) }
                                    Text(app.connected ? "Connected" : "Connect").foregroundStyle(app.connected ? Palette.accent : Palette.text)
                                }.frame(minWidth: 70)
                            }.buttonStyle(CapsuleButtonStyle(primary: false, compact: true))
                                .disabled(app.connecting)
                                .accessibilityLabel(app.connected ? "Connected to \(app.name)" : "Connect \(app.name)")
                        } else if app.id == "claude-desktop" {
                            Link("Get it ↗", destination: URL(string: "https://claude.ai/download")!).font(.system(size: 12, weight: .medium)).foregroundStyle(Palette.text)
                        }
                    }.padding(.horizontal, 17).frame(height: 60)
                        .opacity(app.found ? 1 : 0.55)
                        .animation(reduceMotion ? nil : .spring(response: 0.35, dampingFraction: 0.85), value: app.connected)
                    if app.id != model.apps.last?.id { Rectangle().fill(.white.opacity(0.08)).frame(height: 1).padding(.leading, 62) }
                }
            }.glassPanel()
            HStack(spacing: 10) {
                Image(systemName: "globe").font(.system(size: 18)).foregroundStyle(Palette.secondary)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Claude on the web or phone").font(.system(size: 12, weight: .medium))
                    Text(model.remoteEnabled ? "Remote access is enabled." : "A guided setup opens in Terminal.").font(.system(size: 11)).foregroundStyle(Palette.secondary)
                }
                Spacer()
                Button("Set up ↗") { model.openRemoteSetup() }.buttonStyle(CapsuleButtonStyle(primary: false, compact: true))
            }.padding(.horizontal, 4)
        }
    }
}
struct AppIcon: View {
    let app: AIApp
    @Environment(\.renderingSnapshot) private var snapshot
    var body: some View {
        if !snapshot, let path = app.path, FileManager.default.fileExists(atPath: path) {
            if let cg = NSWorkspace.shared.icon(forFile: path).cgImage(forProposedRect: nil, context: nil, hints: nil) {
                Image(decorative: cg, scale: 1).resizable().scaledToFit()
            } else {
                Image(systemName: app.symbol).resizable().scaledToFit()
            }
        } else {
            ZStack {
                RoundedRectangle(cornerRadius: 8).fill(.white.opacity(0.07))
                Image(systemName: app.symbol).font(.system(size: 17, weight: .medium)).foregroundStyle(Palette.text)
            }
        }
    }
}
