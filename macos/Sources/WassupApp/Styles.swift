import SwiftUI
import AppKit

extension Color {
    init(hex: UInt32) { self.init(.sRGB, red: Double((hex >> 16) & 255) / 255, green: Double((hex >> 8) & 255) / 255, blue: Double(hex & 255) / 255, opacity: 1) }
}
enum Palette {
    static let background = Color(hex: 0x0A0A0A)
    static let surface = Color(hex: 0x141414)
    static let accent = Color(hex: 0x34C77B)
    static let raised = Color(hex: 0x1A1A1A)
    static let elevated = Color(hex: 0x222222)
    static let text = Color(hex: 0xEDEDED)
    static let secondary = text.opacity(0.62)
    static let tertiary = text.opacity(0.40)
    static let blue = Color(hex: 0x53BDEB)
}
private struct SnapshotKey: EnvironmentKey { static let defaultValue = false }
extension EnvironmentValues {
    var renderingSnapshot: Bool { get { self[SnapshotKey.self] } set { self[SnapshotKey.self] = newValue } }
}
struct AppBackground: View {
    var body: some View {
        Palette.background.ignoresSafeArea()
    }
}
struct GlassPanel: ViewModifier {
    var radius: CGFloat = 18
    func body(content: Content) -> some View {
        content.background(RoundedRectangle(cornerRadius: radius).fill(Palette.surface))
            .overlay { RoundedRectangle(cornerRadius: radius).strokeBorder(.white.opacity(0.08), lineWidth: 1) }
    }
}
extension View {
    func glassPanel(radius: CGFloat = 18) -> some View { modifier(GlassPanel(radius: radius)) }
}
struct CapsuleButtonStyle: ButtonStyle {
    var primary = true
    var compact = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.isEnabled) private var enabled
    @State private var hovering = false
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: compact ? 12 : 14, weight: .semibold))
            .foregroundStyle(primary ? Palette.background : Palette.text)
            .padding(.horizontal, compact ? 16 : 25)
            .frame(height: compact ? 31 : 44)
            .background {
                if primary { Capsule().fill(Palette.accent) }
                else { Capsule().fill(Palette.raised) }
            }
            .overlay {
                if primary {
                    Capsule().strokeBorder(.white.opacity(0.08), lineWidth: 1)
                        .mask(alignment: .top) { Rectangle().frame(height: 1) }
                } else { Capsule().strokeBorder(.white.opacity(0.08), lineWidth: 1) }
            }
            .shadow(color: primary ? .black.opacity(0.22) : .clear, radius: 3, y: 2)
            .scaleEffect(reduceMotion ? 1 : (configuration.isPressed ? 0.96 : (hovering && enabled ? 1.02 : 1)))
            .opacity(enabled ? 1 : 0.42)
            .animation(reduceMotion ? .easeOut(duration: 0.15) : .spring(response: 0.3, dampingFraction: 0.75), value: configuration.isPressed)
            .animation(.easeOut(duration: 0.18), value: hovering)
            .onHover { hovering = $0 }
            .contentShape(Capsule())
    }
}
struct CopyButton: View {
    let value: String
    @State private var copied = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        Button {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(value, forType: .string)
            copied = true
        } label: {
            Label(copied ? "Copied" : "Copy code", systemImage: copied ? "checkmark" : "doc.on.doc")
                .contentTransition(reduceMotion ? .opacity : .symbolEffect(.replace))
        }
        .buttonStyle(CapsuleButtonStyle(primary: false, compact: true))
        .animation(reduceMotion ? .easeOut(duration: 0.15) : .spring(response: 0.3), value: copied)
        .task(id: copied) {
            if copied { try? await Task.sleep(for: .milliseconds(1500)); if !Task.isCancelled { copied = false } }
        }
    }
}
struct StepHeading: View {
    let title: String
    let subtitle: String
    var body: some View {
        VStack(spacing: 9) {
            Text(title).font(.system(size: 29, weight: .semibold, design: .rounded)).tracking(-0.65).foregroundStyle(Palette.text)
            Text(subtitle).font(.system(size: 14)).foregroundStyle(Palette.secondary).multilineTextAlignment(.center).lineSpacing(3)
        }.fixedSize(horizontal: false, vertical: true)
    }
}
struct ErrorBanner: View {
    let error: CLIError
    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "exclamationmark.circle.fill").foregroundStyle(Palette.text)
            VStack(alignment: .leading, spacing: 4) {
                Text(error.message).foregroundStyle(Palette.text).fontWeight(.medium)
                if !error.hint.isEmpty { Text(error.hint).foregroundStyle(Palette.secondary) }
            }.font(.system(size: 12)).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }.padding(12).background(.white.opacity(0.04), in: RoundedRectangle(cornerRadius: 12))
    }
}
struct SoftTransition: ViewModifier {
    let active: Bool
    func body(content: Content) -> some View { content.opacity(active ? 0 : 1).offset(y: active ? 10 : 0) }
}
extension AnyTransition {
    @MainActor static let setupStep = AnyTransition.modifier(active: SoftTransition(active: true), identity: SoftTransition(active: false))
}
