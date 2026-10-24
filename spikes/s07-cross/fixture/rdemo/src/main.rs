include!(concat!(env!("OUT_DIR"), "/gen.rs"));

fn main() {
    let mut b = itoa::Buffer::new();
    let n = b.format(std::mem::size_of::<usize>() * 8);
    let mut msg = format!("rdemo target={} answer={} bits={} x86cfg={}", TARGET, rmac::answer!(), n, cfg!(bob_x86));
    if cfg!(feature = "shout") {
        msg = msg.to_uppercase();
    }
    if cfg!(debug_assertions) {
        msg.push_str(" debug");
    }
    println!("{msg}");
}
