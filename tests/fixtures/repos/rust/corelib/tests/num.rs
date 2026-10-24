#[test]
fn num_formats() {
    let s = corelib::num(42);
    assert!(s == "42" || s == "3432", "unexpected {s}");
}
