fn main() {
    println!("cargo:rerun-if-changed=src/computer_use_picker.m");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("macos") {
        cc::Build::new()
            .file("src/computer_use_picker.m")
            .flag("-fobjc-arc")
            .flag("-fblocks")
            .flag("-Werror=unguarded-availability")
            .compile("computer_use_picker");
        // Weak linking keeps the rest of the app available on macOS 12.
        println!("cargo:rustc-link-arg=-Wl,-weak_framework,ScreenCaptureKit");
        println!("cargo:rustc-link-lib=framework=AppKit");
        println!("cargo:rustc-link-lib=framework=CoreMedia");
        println!("cargo:rustc-link-lib=framework=ImageIO");
    }
    tauri_build::build()
}
