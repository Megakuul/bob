//! cdylib used from C.
#[no_mangle]
pub extern "C" fn rdyn_add(a: i64, b: i64) -> i64 {
    a + b
}
