//! Proc-macro (host unit). Uses itoa at expansion time, so itoa is needed on the host too.
use proc_macro::TokenStream;

/// `answer!()` expands to a string literal computed on the build host.
#[proc_macro]
pub fn answer(_input: TokenStream) -> TokenStream {
    let mut b = itoa::Buffer::new();
    let s = b.format(6u32 * 7);
    format!("\"{}\"", s).parse().unwrap()
}
