import SwiftUI
import AppKit

struct SetupView: View {
    @Bindable var model: AppModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Namespace private var steps
    private var canContinue: Bool {
        if !model.ready && !model.snapshotMode { return false }
        if model.busy || model.apps.contains(where: \.connecting) { return false }
        if model.step == .link { return model.status.loggedIn }
        if model.step == .safety { return model.approvalLoaded }
        return true
    }
    var body: some View {
        ZStack {
            AppBackground()
            VStack(spacing: 0) {
                HStack {
                    HStack(spacing: 5) { WassupLogo().frame(width: 25, height: 25); Text("wassup").font(.system(size: 14, weight: .semibold, design: .rounded)) }
                    Spacer()
                    if model.step != .welcome && model.step != .done {
                        HStack(spacing: 6) {
                            ForEach(1...4, id: \.self) { index in
                                ZStack {
                                    Capsule().fill(.white.opacity(0.10))
                                    if min(model.step.rawValue, 4) == index {
                                        if reduceMotion { Capsule().fill(Palette.accent) }
                                        else { Capsule().fill(Palette.accent).matchedGeometryEffect(id: "active", in: steps) }
                                    }
                                }.frame(width: min(model.step.rawValue, 4) == index ? 26 : 7, height: 5)
                            }
                        }.transaction { if reduceMotion { $0.animation = nil } }.accessibilityLabel("Step \(min(model.step.rawValue, 4)) of 4")
                    } else {
                        Text("Made for your Mac").font(.system(size: 11)).foregroundStyle(Palette.secondary)
                    }
                }.frame(height: 28)
                ZStack {
                    Group {
                        switch model.step {
                        case .welcome: WelcomeView()
                        case .link: LinkView(model: model)
                        case .running: RunningView(model: model)
                        case .connect: ConnectView(model: model)
                        case .safety: SafetyView(model: model)
                        case .done: DoneView(model: model)
                        }
                    }.id(model.step).transition(reduceMotion ? .opacity : .setupStep)
                }.frame(maxWidth: .infinity, maxHeight: .infinity)
                if let error = model.error, model.step != .link {
                    ErrorBanner(error: error).padding(.bottom, 14)
                }
                HStack {
                    if model.step != .welcome && model.step != .done {
                        Button { model.back() } label: { Label("Back", systemImage: "chevron.left") }
                            .buttonStyle(.plain).font(.system(size: 13)).foregroundStyle(Palette.secondary)
                            .disabled(model.busy || model.apps.contains(where: \.connecting))
                    } else {
                        Label(model.step == .done ? "Ready in your menu bar" : "Your account. Your control.", systemImage: model.step == .done ? "menubar.rectangle" : "lock.shield")
                            .font(.system(size: 11)).foregroundStyle(Palette.secondary)
                    }
                    Spacer()
                    if model.step == .done && FileManager.default.fileExists(atPath: "/Applications/Claude.app") {
                        Button("Open Claude") { NSWorkspace.shared.open(URL(fileURLWithPath: "/Applications/Claude.app")) }.buttonStyle(CapsuleButtonStyle(primary: false))
                    }
                    if !model.ready && model.error != nil && !model.busy {
                        Button("Try again") { Task { await model.bootstrap() } }.buttonStyle(CapsuleButtonStyle(primary: false))
                    }
                    Button { model.advance() } label: {
                        HStack(spacing: 9) {
                            if model.busy { ProgressView().controlSize(.small).tint(Palette.background) }
                            Text(model.step == .welcome ? "Get started" : model.step == .done ? "Done" : "Continue")
                            if model.step != .done { Image(systemName: "arrow.right").font(.system(size: 11, weight: .bold)) }
                        }
                    }.buttonStyle(CapsuleButtonStyle()).disabled(!canContinue)
                        .keyboardShortcut(.defaultAction)
                }.frame(height: 44)
            }.padding(.horizontal, 48).padding(.top, 34).padding(.bottom, 32)
        }.frame(width: 680, height: 580)
            .foregroundStyle(Palette.text).preferredColorScheme(.dark)
            .animation(reduceMotion ? .easeOut(duration: 0.2) : .spring(response: 0.5, dampingFraction: 0.85), value: model.step)
    }
}
struct WelcomeView: View {
    var body: some View {
        VStack(spacing: 0) {
            AnimatedLogo().frame(width: 154, height: 154).padding(.bottom, 22)
            Text("WhatsApp for your AI").font(.system(size: 37, weight: .semibold, design: .rounded)).tracking(-1.15)
            Text("Let your AI catch you up and send messages, with you in control.")
                .font(.system(size: 15)).foregroundStyle(Palette.secondary).multilineTextAlignment(.center)
                .lineSpacing(4).frame(maxWidth: 405).padding(.top, 13)
            HStack(spacing: 6) {
                Image(systemName: "checkmark").foregroundStyle(Palette.blue); Image(systemName: "checkmark").foregroundStyle(Palette.blue).padding(.leading, -10)
                Text("Nothing sends until you say yes.").padding(.leading, 3)
            }.font(.system(size: 11)).foregroundStyle(Palette.secondary).padding(.top, 32)
        }.padding(.bottom, 10)
    }
}
struct RunningView: View {
    @Bindable var model: AppModel
    @Environment(\.renderingSnapshot) private var snapshot
    var body: some View {
        VStack(spacing: 26) {
            ZStack {
                RoundedRectangle(cornerRadius: 21).fill(.white.opacity(0.04)).frame(width: 82, height: 76)
                Image(systemName: "power").font(.system(size: 34, weight: .light)).foregroundStyle(Palette.text)
            }
            StepHeading(title: "Keep it running.", subtitle: "Ready for your AI, even after you restart your Mac.")
            HStack(spacing: 16) {
                Image(systemName: "sun.horizon").font(.system(size: 23)).foregroundStyle(Palette.secondary).frame(width: 36)
                VStack(alignment: .leading, spacing: 5) {
                    Text("Start wassup when I log in").font(.system(size: 15, weight: .medium))
                    Text(model.serviceInstalled ? "Already set up on this Mac." : "It stays quietly in the background.").font(.system(size: 12)).foregroundStyle(Palette.secondary)
                }
                Spacer()
                if snapshot {
                    Capsule().fill(Palette.accent).frame(width: 38, height: 22)
                        .overlay(alignment: model.startAtLogin ? .trailing : .leading) { Circle().fill(.white).frame(width: 18, height: 18).padding(2) }
                } else {
                    Toggle("Start wassup when I log in", isOn: $model.startAtLogin).labelsHidden().toggleStyle(.switch).tint(Palette.accent)
                }
            }.padding(21).glassPanel().padding(.top, 4)
            Text("Close this window anytime. wassup lives in your menu bar.")
                .font(.system(size: 12)).foregroundStyle(Palette.secondary)
        }
    }
}
struct DoneView: View {
    let model: AppModel
    var body: some View {
        VStack(spacing: 20) {
            SuccessMark().frame(width: 78, height: 78)
            StepHeading(title: "You're set.", subtitle: "Your WhatsApp is ready for a helping hand.")
            VStack(alignment: .leading, spacing: 10) {
                Text("Try asking Claude").font(.system(size: 11, weight: .medium)).foregroundStyle(Palette.secondary)
                HStack(alignment: .bottom) {
                    Text("catch me up on WhatsApp").font(.system(size: 20, weight: .medium, design: .rounded))
                    Spacer(minLength: 12)
                    Image(systemName: "checkmark").overlay { Image(systemName: "checkmark").offset(x: 5) }.foregroundStyle(Palette.blue)
                }
            }.padding(21).background(Palette.surface, in: UnevenRoundedRectangle(topLeadingRadius: 20, bottomLeadingRadius: 20, bottomTrailingRadius: 5, topTrailingRadius: 20))
                .overlay { UnevenRoundedRectangle(topLeadingRadius: 20, bottomLeadingRadius: 20, bottomTrailingRadius: 5, topTrailingRadius: 20).strokeBorder(.white.opacity(0.08), lineWidth: 1) }
                .padding(.top, 5)
            Text(model.notificationNote.isEmpty ? (model.approval == .never ? "Your AI can send. You can change approvals anytime in Set up." : "Approvals are one click away in your menu bar.") : model.notificationNote)
                .font(.system(size: 12)).foregroundStyle(Palette.secondary).multilineTextAlignment(.center)
        }.padding(.horizontal, 28)
    }
}
