// Build script: runs on the host, describes the target.
use std::{env, fs, path::Path};

fn main() {
    let target = env::var("TARGET").unwrap();
    let arch = env::var("CARGO_CFG_TARGET_ARCH").unwrap();
    let out = env::var("OUT_DIR").unwrap();
    fs::write(Path::new(&out).join("gen.rs"), format!("pub const TARGET: &str = {:?};\n", target)).unwrap();
    if arch == "x86_64" {
        println!("cargo:rustc-cfg=bob_x86");
    }
    if env::var("CARGO_FEATURE_SHOUT").is_ok() {
        println!("cargo:rustc-cfg=bob_shout");
    }
}
