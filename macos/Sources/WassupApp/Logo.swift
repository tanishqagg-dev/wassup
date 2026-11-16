import SwiftUI

struct BubbleShape: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: 34, y: 15))
        p.addLine(to: CGPoint(x: 66, y: 15))
        p.addCurve(to: CGPoint(x: 86, y: 35), control1: CGPoint(x: 77.046, y: 15), control2: CGPoint(x: 86, y: 23.954))
        p.addLine(to: CGPoint(x: 86, y: 52))
        p.addCurve(to: CGPoint(x: 66, y: 72), control1: CGPoint(x: 86, y: 63.046), control2: CGPoint(x: 77.046, y: 72))
        p.addLine(to: CGPoint(x: 41, y: 72))
        p.addLine(to: CGPoint(x: 25.5, y: 84.4))
        p.addCurve(to: CGPoint(x: 22.3, y: 82.1), control1: CGPoint(x: 23.9, y: 85.7), control2: CGPoint(x: 21.6, y: 84.1))
        p.addLine(to: CGPoint(x: 26, y: 70.5))
        p.addCurve(to: CGPoint(x: 14, y: 52), control1: CGPoint(x: 18.72, y: 67.32), control2: CGPoint(x: 14, y: 60.11))
        p.addLine(to: CGPoint(x: 14, y: 35))
        p.addCurve(to: CGPoint(x: 34, y: 15), control1: CGPoint(x: 14, y: 23.954), control2: CGPoint(x: 22.954, y: 15))
        p.closeSubpath()
        return p.applying(CGAffineTransform(scaleX: rect.width / 100, y: rect.height / 100))
    }
}
struct WShape: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: 29, y: 37))
        for point in [CGPoint(x: 37.5, y: 53), CGPoint(x: 50, y: 37), CGPoint(x: 62.5, y: 53), CGPoint(x: 71, y: 37)] { p.addLine(to: point) }
        return p.applying(CGAffineTransform(scaleX: rect.width / 100, y: rect.height / 100))
    }
}
struct WassupLogo: View {
    var progress: CGFloat = 1
    var body: some View {
        GeometryReader { proxy in
            ZStack {
                BubbleShape().fill(Palette.accent)
                WShape().trim(from: 0, to: progress)
                    .stroke(.white, style: StrokeStyle(lineWidth: proxy.size.width * 0.08, lineCap: .round, lineJoin: .round))
                    .blendMode(.destinationOut)
            }.compositingGroup()
        }.accessibilityLabel("wassup")
    }
}
struct AnimatedLogo: View {
    @Environment(\.renderingSnapshot) private var snapshot
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var arrived = false
    @State private var drawn: CGFloat = 0
    var body: some View {
        WassupLogo(progress: snapshot || reduceMotion ? 1 : drawn)
            .scaleEffect(snapshot || reduceMotion || arrived ? 1 : 0.6)
            .opacity(snapshot || arrived ? 1 : 0)
            .task {
                guard !snapshot else { return }
                if reduceMotion { withAnimation(.easeOut(duration: 0.25)) { arrived = true }; return }
                withAnimation(.spring(response: 0.65, dampingFraction: 0.72)) { arrived = true }
                try? await Task.sleep(for: .milliseconds(330))
                guard !Task.isCancelled else { return }
                withAnimation(.easeInOut(duration: 0.55)) { drawn = 1 }

            }
    }
}
struct CheckShape: Shape {
    func path(in r: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: r.width * 0.22, y: r.height * 0.51))
        p.addLine(to: CGPoint(x: r.width * 0.42, y: r.height * 0.71))
        p.addLine(to: CGPoint(x: r.width * 0.79, y: r.height * 0.31))
        return p
    }
}
struct SuccessMark: View {
    @Environment(\.renderingSnapshot) private var snapshot
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var drawn = false
    var body: some View {
        ZStack {
            Circle().fill(.white.opacity(0.04))
            Circle().strokeBorder(.white.opacity(0.08), lineWidth: 1)
            CheckShape().trim(from: 0, to: snapshot || reduceMotion || drawn ? 1 : 0)
                .stroke(Palette.accent, style: StrokeStyle(lineWidth: 4, lineCap: .round, lineJoin: .round))
        }.accessibilityLabel("Linked successfully")
            .onAppear { withAnimation(reduceMotion ? .easeOut(duration: 0.2) : .easeOut(duration: 0.65)) { drawn = true } }
    }
}
