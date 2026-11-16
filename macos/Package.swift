// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "wassup-macos",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "WassupApp", targets: ["WassupApp"])],
    targets: [
        .executableTarget(name: "WassupApp"),
        .testTarget(name: "WassupAppTests", dependencies: ["WassupApp"])
    ],
    swiftLanguageModes: [.v6]
)
