#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .with_writer(std::io::stderr)
        .init();

    if let Err(error) = kbase_lance_engine::run().await {
        tracing::error!(error = %error, "kbase-lance-engine failed");
        std::process::exit(1);
    }
}
