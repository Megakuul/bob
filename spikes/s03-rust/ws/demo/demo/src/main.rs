//! Demo binary exercising build scripts, proc-macros (crates.io + local), C code (zstd-sys) and tokio.
use clap::Parser;
use mymacro::Describe;
use serde::{Deserialize, Serialize};

#[derive(Parser, Debug)]
struct Args {
    /// Number to format
    #[arg(long, default_value_t = 42)]
    n: u64,
}

#[derive(Serialize, Deserialize, Describe, Debug)]
struct Point {
    x: i32,
    y: i32,
}

#[derive(thiserror::Error, Debug)]
enum DemoError {
    #[error("bad: {0}")]
    Bad(String),
}

#[tokio::main(flavor = "current_thread")]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    tokio::time::sleep(std::time::Duration::from_millis(1)).await;
    let p = Point { x: 1, y: 2 };
    let js = serde_json::to_string(&p)?;
    let back: Point = serde_json::from_str(&js)?;
    let re = regex::Regex::new(r"^\{.x.:1").unwrap();
    let data = js.repeat(50);
    let z = zstd::encode_all(data.as_bytes(), 3)?;
    let unz = zstd::decode_all(&z[..])?;
    assert_eq!(unz, data.as_bytes());
    let pid = unsafe { libc::getpid() } > 0;
    let e: Result<(), DemoError> = Err(DemoError::Bad("x".into()));
    log::info!("not printed");
    println!(
        "json={js} back.y={} re={} zstd={}->{} {} pid_ok={pid} corelib={} {} {} target={} err={}",
        back.y,
        re.is_match(&js),
        data.len(),
        z.len(),
        Point::describe(),
        corelib::num(args.n),
        corelib::mode(),
        corelib::BUILT_BY,
        corelib::TARGET,
        e.unwrap_err()
    );
    Ok(())
}
