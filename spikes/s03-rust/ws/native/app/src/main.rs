//! Uses the nixpkgs OpenSSL through openssl-sys (pkg-config in its build script) and the
//! openssl crate, whose build script reads DEP_OPENSSL_* metadata from openssl-sys.
use openssl::sha::sha256;
fn main() {
    let d = sha256(b"bob");
    let hex: String = d.iter().map(|b| format!("{b:02x}")).collect();
    println!("openssl {} sha256(bob)={hex}", openssl::version::version());
}
