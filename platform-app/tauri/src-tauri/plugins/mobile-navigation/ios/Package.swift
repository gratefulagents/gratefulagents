// swift-tools-version:5.3

import PackageDescription

let package = Package(
    name: "tauri-plugin-mobile-navigation",
    platforms: [
        .iOS(.v13),
    ],
    products: [
        .library(
            name: "tauri-plugin-mobile-navigation",
            type: .static,
            targets: ["MobileNavigationPlugin"]),
    ],
    dependencies: [
        .package(name: "Tauri", path: "../.tauri/tauri-api")
    ],
    targets: [
        .target(
            name: "MobileNavigationPlugin",
            dependencies: [
                .byName(name: "Tauri")
            ],
            path: "Sources/MobileNavigationPlugin")
    ]
)
