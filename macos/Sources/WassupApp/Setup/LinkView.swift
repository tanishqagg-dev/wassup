import SwiftUI
import CoreImage.CIFilterBuiltins
import AppKit

struct QRCodeView: View {
    let code: String
    private var image: NSImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(code.utf8)
        filter.correctionLevel = "L"
        guard let output = filter.outputImage, let cg = CIContext().createCGImage(output, from: output.extent) else { return nil }
        return NSImage(cgImage: cg, size: output.extent.size)
    }
    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 17).fill(.white)
            if let image {
                Image(nsImage: image).interpolation(.none).resizable().scaledToFit().padding(15)
            } else { Image(systemName: "qrcode").font(.system(size: 64)).foregroundStyle(.black) }
        }.accessibilityLabel("Scan this QR code with WhatsApp to link your phone")
    }
}
struct LinkView: View {
    @Bindable var model: AppModel
    @Environment(\.renderingSnapshot) private var snapshot
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        VStack(spacing: 23) {
            if model.status.loggedIn {
                Spacer(minLength: 20)
                SuccessMark().frame(width: 92, height: 92)
                StepHeading(title: "Linked as \(model.status.displayName)", subtitle: "Your phone and this Mac are connected.")
                Label("You're good to go", systemImage: "checkmark.shield").font(.system(size: 12)).foregroundStyle(Palette.accent)
                Spacer(minLength: 30)
            } else {
                StepHeading(title: "Link your phone.", subtitle: model.phoneMode ? "Same WhatsApp. A simple code instead of a scan." : "One quick scan, and your AI can start helping.")
                if let error = model.error {
                    VStack(spacing: 20) {
                        Image(systemName: "wifi.exclamationmark").font(.system(size: 38, weight: .light)).foregroundStyle(Palette.text)
                        ErrorBanner(error: error)
                        Button("Try again") { model.beginLogin() }.buttonStyle(CapsuleButtonStyle(primary: false))
                    }.frame(maxWidth: .infinity, minHeight: 210)
                } else if model.phoneMode {
                    phoneContent
                } else {
                    qrContent
                }
                Button(model.phoneMode ? "Use a QR code instead" : "Use phone number instead") { model.switchLoginMode() }
                    .buttonStyle(.plain).foregroundStyle(Palette.text).font(.system(size: 12, weight: .medium))
            }
        }
        .animation(reduceMotion ? .easeOut(duration: 0.2) : .spring(response: 0.5, dampingFraction: 0.85), value: model.status.loggedIn)
        .animation(.easeOut(duration: 0.2), value: model.phoneMode)
    }
    private var qrContent: some View {
        HStack(alignment: .center, spacing: 31) {
            VStack(spacing: 13) {
                ZStack {
                    if model.qrCode.isEmpty {
                        RoundedRectangle(cornerRadius: 17).fill(.white.opacity(0.04))
                        VStack(spacing: 12) { ProgressView().tint(Palette.secondary); Text("Getting your code…").font(.system(size: 12)).foregroundStyle(Palette.secondary) }
                    } else {
                        QRCodeView(code: model.qrCode).id(model.qrCode)
                            .transition(reduceMotion ? .opacity : .opacity.combined(with: .scale(scale: 0.96)))
                    }
                }.frame(width: 214, height: 214)
                    .animation(.easeInOut(duration: 0.25), value: model.qrCode)
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    let seconds = snapshot ? 52 : max(0, Int(ceil(model.qrExpires.timeIntervalSince(context.date))))
                    HStack(spacing: 7) {
                        ZStack {
                            Circle().stroke(.white.opacity(0.08), lineWidth: 2)
                            Circle().trim(from: 0, to: min(1, Double(seconds) / model.qrLifetime)).stroke(Palette.secondary, style: StrokeStyle(lineWidth: 2, lineCap: .round)).rotationEffect(.degrees(-90))
                        }.frame(width: 13, height: 13)
                        Text(model.qrCode.isEmpty ? "Waiting for a code" : seconds == 0 ? "Refreshing code…" : "New code in \(seconds)s")
                            .font(.system(size: 11)).monospacedDigit().foregroundStyle(Palette.secondary)
                    }
                }
            }
            VStack(alignment: .leading, spacing: 25) {
                instruction("1", symbol: "message", text: "Open WhatsApp", detail: "On your phone")
                instruction("2", symbol: "gearshape", text: "Settings › Linked devices", detail: "On Android, tap the ⋮ menu")
                instruction("3", symbol: "qrcode.viewfinder", text: "Link a device and scan", detail: "Point your camera at this code")
            }.frame(maxWidth: .infinity, alignment: .leading)
        }.padding(.vertical, 3)
    }
    private var phoneContent: some View {
        VStack(spacing: 21) {
            if model.pairCode.isEmpty {
                Image(systemName: "iphone").font(.system(size: 40, weight: .ultraLight)).foregroundStyle(Palette.text).padding(.top, 10)
                HStack(spacing: 12) {
                    if snapshot {
                        Text("+91 98765 43210").foregroundStyle(Palette.secondary).frame(maxWidth: .infinity, alignment: .leading).padding(14).glassPanel(radius: 12)
                    } else {
                        TextField("+91 98765 43210", text: $model.phoneNumber)
                            .textFieldStyle(.plain).font(.system(size: 16)).padding(14).glassPanel(radius: 12)
                            .accessibilityLabel("Phone number with country code")
                            .onSubmit { if !model.loginRunning { model.beginLogin() } }
                    }
                    Button { model.beginLogin() } label: {
                        if model.loginRunning { ProgressView().controlSize(.small) } else { Text("Get code") }
                    }.buttonStyle(CapsuleButtonStyle()).disabled(model.phoneNumber.isEmpty || model.loginRunning)
                }.frame(maxWidth: 450)
                Text("Include your country code, starting with +.").font(.system(size: 12)).foregroundStyle(Palette.secondary)
            } else {
                Text("Enter this code on your phone").font(.system(size: 12)).foregroundStyle(Palette.secondary)
                HStack(spacing: 7) {
                    ForEach(Array(model.pairCode.filter { $0 != "-" }.enumerated()), id: \.offset) { index, character in
                        PairTile(character: String(character), index: index).padding(.leading, index == 4 ? 9 : 0)
                    }
                }
                CopyButton(value: model.pairCode)
                VStack(spacing: 6) {
                    Text("Linked devices › Link a device").foregroundStyle(Palette.text)
                    Text("Choose \"Link with phone number instead\".").foregroundStyle(Palette.secondary)
                }.font(.system(size: 13)).multilineTextAlignment(.center)
            }
        }.frame(height: 246)
    }
    private func instruction(_ number: String, symbol: String, text: String, detail: String) -> some View {
        HStack(alignment: .top, spacing: 13) {
            ZStack {
                Circle().fill(.white.opacity(0.05)).frame(width: 28, height: 28)
                Text(number).font(.system(size: 12, weight: .medium)).foregroundStyle(Palette.secondary)
            }
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) { Image(systemName: symbol).font(.system(size: 12)); Text(text).font(.system(size: 13, weight: .medium)) }
                Text(detail).font(.system(size: 11)).foregroundStyle(Palette.secondary)
            }.padding(.top, 2)
        }
    }
}
struct PairTile: View {
    let character: String
    let index: Int
    @Environment(\.renderingSnapshot) private var snapshot
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var appeared = false
    var body: some View {
        Text(character).font(.system(size: 29, weight: .medium, design: .monospaced))
            .frame(width: 45, height: 60).glassPanel(radius: 11)
            .opacity(snapshot || appeared ? 1 : 0)
            .offset(y: snapshot || reduceMotion || appeared ? 0 : 8)
            .onAppear { withAnimation(reduceMotion ? .easeOut(duration: 0.15) : .spring(response: 0.4).delay(Double(index) * 0.045)) { appeared = true } }
    }
}
