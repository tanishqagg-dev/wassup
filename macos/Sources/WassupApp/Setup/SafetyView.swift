import SwiftUI

struct SafetyView: View {
    @Bindable var model: AppModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        VStack(spacing: 20) {
            StepHeading(title: "How careful should your AI be?", subtitle: "You decide when it needs your say-so.")
            VStack(spacing: 10) {
                ForEach(ApprovalMode.allCases) { mode in
                    Button { model.approval = mode } label: {
                        HStack(spacing: 15) {
                            Image(systemName: mode.symbol).font(.system(size: 21, weight: .light)).foregroundStyle(model.approval == mode ? Palette.accent : Palette.secondary).frame(width: 29)
                            VStack(alignment: .leading, spacing: 5) {
                                HStack(spacing: 8) {
                                    Text(mode.title).font(.system(size: 14, weight: .semibold))
                                    if mode == .always { Text("Recommended").font(.system(size: 9, weight: .medium)).foregroundStyle(Palette.secondary).padding(.horizontal, 7).padding(.vertical, 3).background(.white.opacity(0.04), in: Capsule()) }
                                }
                                Text(mode.detail).font(.system(size: 11)).foregroundStyle(Palette.secondary)
                            }
                            Spacer(minLength: 0)
                            ZStack {
                                Circle().strokeBorder(model.approval == mode ? Palette.accent : .white.opacity(0.08), lineWidth: 1.5).frame(width: 21, height: 21)
                                if model.approval == mode { Circle().fill(Palette.accent).frame(width: 21, height: 21); Image(systemName: "checkmark").font(.system(size: 10, weight: .bold)).foregroundStyle(Palette.background) }
                            }
                        }.padding(.horizontal, 18).frame(height: 72)
                            .background(model.approval == mode ? .white.opacity(0.05) : .white.opacity(0.025), in: RoundedRectangle(cornerRadius: 15))
                            .overlay { RoundedRectangle(cornerRadius: 15).strokeBorder(model.approval == mode ? Palette.accent : .white.opacity(0.08), lineWidth: model.approval == mode ? 1.5 : 1) }
                            .contentShape(RoundedRectangle(cornerRadius: 15))
                    }.buttonStyle(.plain).accessibilityAddTraits(model.approval == mode ? [.isSelected] : [])
                }
            }.animation(reduceMotion ? .easeOut(duration: 0.15) : .spring(response: 0.35, dampingFraction: 0.8), value: model.approval)
            HStack(alignment: .top, spacing: 9) {
                Image(systemName: "menubar.rectangle").font(.system(size: 16)).foregroundStyle(Palette.secondary)
                Text("Approvals appear in this Mac's menu bar, as a notification, and in your WhatsApp \"Message yourself\" chat. Reply yes 1234 to approve there.")
                    .font(.system(size: 11)).foregroundStyle(Palette.secondary).lineSpacing(3)
            }.padding(.horizontal, 6)
            if !model.approvalLoaded && !model.busy {
                Button("Load current setting again") { Task { await model.readApproval() } }.buttonStyle(.plain).foregroundStyle(Palette.text).font(.system(size: 12))
            }
        }
    }
}
