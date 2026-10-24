// Local build script: emits a cfg, an env var and a generated file in OUT_DIR.
use std::{env, fs, path::Path};

fn main() {
    let out = env::var("OUT_DIR").unwrap();
    let target = env::var("TARGET").unwrap();
    let fast = env::var("CARGO_FEATURE_FAST").is_ok();
    fs::write(Path::new(&out).join("gen.rs"), format!("pub const TARGET: &str = {target:?};\n")).unwrap();
    if fast {
        println!("cargo::rustc-cfg=corelib_fast");
    }
    println!("cargo::rustc-check-cfg=cfg(corelib_fast)");
    println!("cargo::rustc-env=CORELIB_BUILT_BY=build.rs");
    println!("cargo::rerun-if-changed=build.rs");
}
