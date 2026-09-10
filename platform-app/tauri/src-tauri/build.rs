fn main() {
    println!("cargo:rerun-if-changed=src/computer_use_picker.m");
    if std::env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("macos") {
        let mut native = cc::Build::new();
        native
            .file("src/computer_use_picker.m")
            .flag("-fobjc-arc")
            .flag("-fblocks")
            .flag("-Werror=unguarded-availability")
            .compile("computer_use_picker");

        // Rust links with -nodefaultlibs, so Clang's @available runtime must be explicit.
        let resource_dir = native
            .get_compiler()
            .to_command()
            .arg("-print-resource-dir")
            .output()
            .expect("failed to query Clang's resource directory");
        assert!(
            resource_dir.status.success(),
            "Clang resource directory query failed: {}",
            String::from_utf8_lossy(&resource_dir.stderr)
        );
        let resource_dir =
            String::from_utf8(resource_dir.stdout).expect("Clang resource directory is not UTF-8");
        let runtime_dir = std::path::Path::new(resource_dir.trim()).join("lib/darwin");
        assert!(
            runtime_dir.join("libclang_rt.osx.a").is_file(),
            "Clang macOS runtime not found in {}",
            runtime_dir.display()
        );
        println!("cargo:rustc-link-search=native={}", runtime_dir.display());
        println!("cargo:rustc-link-lib=static=clang_rt.osx");

        // Weak linking keeps the rest of the app available on macOS 12.
        println!("cargo:rustc-link-arg=-Wl,-weak_framework,ScreenCaptureKit");
        println!("cargo:rustc-link-lib=framework=AppKit");
        println!("cargo:rustc-link-lib=framework=CoreMedia");
        println!("cargo:rustc-link-lib=framework=ImageIO");
    }
    tauri_build::build()
}
