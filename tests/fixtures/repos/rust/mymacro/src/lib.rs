//! Local proc-macro: #[derive(Describe)] adds `fn describe() -> &'static str`.
use proc_macro::TokenStream;
use quote::quote;
use syn::{parse_macro_input, DeriveInput};

#[proc_macro_derive(Describe)]
pub fn derive_describe(input: TokenStream) -> TokenStream {
    let ast = parse_macro_input!(input as DeriveInput);
    let name = &ast.ident;
    let fields = match &ast.data {
        syn::Data::Struct(s) => s.fields.len(),
        _ => 0,
    };
    let text = format!("{name} has {fields} fields");
    let span = proc_macro2::Span::call_site();
    let lit = syn::LitStr::new(&text, span);
    quote! {
        impl #name {
            pub fn describe() -> &'static str { #lit }
        }
    }
    .into()
}
