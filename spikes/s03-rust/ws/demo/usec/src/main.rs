//! Rust binary using a bob C library (cfoo, static).
extern "C" {
    fn cfoo_mul(a: i32, b: i32) -> i32;
}
fn main() {
    println!("cfoo_mul(6,7)={}", unsafe { cfoo_mul(6, 7) });
}
