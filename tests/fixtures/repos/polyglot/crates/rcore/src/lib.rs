//! Small Rust staticlib used from Go via cgo. Uses two crates.io crates.
use std::ffi::{c_char, CStr};

/// Formats `n` with itoa and hex-encodes the result into `out` (NUL-terminated).
/// Returns the number of bytes written (without NUL) or -1 if `out` is too small.
#[no_mangle]
pub extern "C" fn rcore_num_hex(n: u64, out: *mut c_char, cap: usize) -> i64 {
    let mut buf = itoa::Buffer::new();
    let encoded = hex::encode(buf.format(n));
    if out.is_null() || encoded.len() + 1 > cap {
        return -1;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(encoded.as_ptr(), out as *mut u8, encoded.len());
        *out.add(encoded.len()) = 0;
    }
    encoded.len() as i64
}

/// Returns the length of a C string, to prove the boundary works both ways.
#[no_mangle]
pub extern "C" fn rcore_strlen(s: *const c_char) -> i64 {
    if s.is_null() {
        return -1;
    }
    unsafe { CStr::from_ptr(s) }.to_bytes().len() as i64
}

#[cfg(test)]
mod tests {
    #[test]
    fn num_hex_42() {
        let mut out = [0 as std::ffi::c_char; 16];
        let n = super::rcore_num_hex(42, out.as_mut_ptr(), out.len());
        assert_eq!(n, 4);
        let s = unsafe { std::ffi::CStr::from_ptr(out.as_ptr()) };
        assert_eq!(s.to_str().unwrap(), "3432");
    }
}
