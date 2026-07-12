pub mod error;
pub mod model;
pub mod server;
pub mod store;

pub use server::{AppState, build_router, run};

pub const ENGINE_VERSION: &str = env!("CARGO_PKG_VERSION");
pub const LANCEDB_VERSION: &str = "0.30.0";
pub const PROTOCOL_VERSION: u32 = 1;
