//! Local library with a build script and features.
include!(concat!(env!("OUT_DIR"), "/gen.rs"));

pub const BUILT_BY: &str = env!("CORELIB_BUILT_BY");
pub const VERSION: &str = env!("CARGO_PKG_VERSION");

pub fn mode() -> &'static str {
    if cfg!(corelib_fast) { "fast" } else { "slow" }
}

pub fn num(n: u64) -> String {
    let mut b = itoa::Buffer::new();
    let s = b.format(n).to_string();
    #[cfg(feature = "hexout")]
    {
        return hex::encode(s);
    }
    #[allow(unreachable_code)]
    s
}

#[cfg(test)]
mod tests {
    #[test]
    fn mode_is_fast_by_default() {
        assert_eq!(super::mode(), "fast");
    }
}
