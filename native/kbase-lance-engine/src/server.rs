use std::{
    env,
    io::Write,
    net::SocketAddr,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};

use axum::{
    Json, Router,
    body::Bytes,
    extract::{DefaultBodyLimit, State},
    http::{HeaderMap, Request, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use serde::Serialize;
use tokio::sync::{Mutex, oneshot};

use crate::{
    ENGINE_VERSION, LANCEDB_VERSION, PROTOCOL_VERSION,
    error::{EngineError, EngineResult},
    model::*,
    store::{Engine, chunks_from_arrow_ipc},
};

const MAX_BODY_BYTES: usize = 64 * 1024 * 1024;

#[derive(Clone)]
pub struct AppState {
    pub engine: Engine,
    token: Arc<str>,
    shutdown_tx: Arc<Mutex<Option<oneshot::Sender<()>>>>,
    shutting_down: Arc<AtomicBool>,
}

impl AppState {
    pub fn new(
        token: String,
        allowed_roots: Vec<PathBuf>,
        shutdown_tx: oneshot::Sender<()>,
    ) -> EngineResult<Self> {
        if token.len() < 32 {
            return Err(EngineError::invalid(
                "KBASE_LANCE_TOKEN must contain at least 32 characters",
            ));
        }
        Ok(Self {
            engine: Engine::new(allowed_roots)?,
            token: token.into(),
            shutdown_tx: Arc::new(Mutex::new(Some(shutdown_tx))),
            shutting_down: Arc::new(AtomicBool::new(false)),
        })
    }

    async fn request_shutdown(&self) {
        if !self.shutting_down.swap(true, Ordering::SeqCst)
            && let Some(sender) = self.shutdown_tx.lock().await.take()
        {
            let _ = sender.send(());
        }
    }
}

pub fn build_router(state: AppState) -> Router {
    Router::new()
        .route("/v1/health", get(health))
        .route("/v1/generations/create", post(create_generation))
        .route("/v1/generations/release", post(release_generation))
        .route("/v1/generations/import", post(import_chunks))
        .route("/v1/generations/validate", post(validate_generation))
        .route("/v1/indexes/build", post(build_indexes))
        .route("/v1/chunks/replace-file", post(replace_file))
        .route("/v1/chunks/delete-file", post(delete_file))
        .route("/v1/search", post(search))
        .route("/v1/read/chunk", post(read_chunk))
        .route("/v1/read/path", post(read_path))
        .route("/v1/stats", post(stats))
        .route("/v1/optimize", post(optimize))
        .route("/v1/shutdown", post(shutdown))
        .layer(DefaultBodyLimit::max(MAX_BODY_BYTES))
        .layer(middleware::from_fn_with_state(state.clone(), authorize))
        .with_state(state)
}

pub async fn run() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let token = env::var("KBASE_LANCE_TOKEN")
        .map_err(|_| "KBASE_LANCE_TOKEN is required and must be passed by the Go supervisor")?;
    let allowed_roots = env::var_os("KBASE_LANCE_ALLOWED_ROOTS")
        .map(|value| env::split_paths(&value).collect::<Vec<_>>())
        .unwrap_or_default();
    let listen_address = env::var("KBASE_LANCE_LISTEN_ADDR")
        .or_else(|_| env::var("KBASE_LANCE_LISTEN"))
        .unwrap_or_else(|_| "127.0.0.1:0".to_owned())
        .parse::<SocketAddr>()?;
    if !listen_address.ip().is_loopback() {
        return Err("KBASE_LANCE_LISTEN_ADDR must use a loopback address".into());
    }
    let parent_pid = env::var("KBASE_LANCE_PARENT_PID")
        .ok()
        .map(|value| value.parse::<u32>())
        .transpose()
        .map_err(|_| "KBASE_LANCE_PARENT_PID must be an unsigned integer")?;

    let listener = tokio::net::TcpListener::bind(listen_address).await?;
    let actual_address = listener.local_addr()?;
    let (shutdown_tx, shutdown_rx) = oneshot::channel();
    let state = AppState::new(token, allowed_roots, shutdown_tx)?;

    let handshake = ReadyHandshake {
        protocol_version: PROTOCOL_VERSION,
        engine_version: ENGINE_VERSION,
        lancedb_version: LANCEDB_VERSION,
        listen_address: actual_address.to_string(),
    };
    println!("{}", serde_json::to_string(&handshake)?);
    std::io::stdout().flush()?;

    if let Some(parent_pid) = parent_pid {
        spawn_parent_watchdog(state.clone(), parent_pid);
    }
    let signal_state = state.clone();
    tokio::spawn(async move {
        if tokio::signal::ctrl_c().await.is_ok() {
            signal_state.request_shutdown().await;
        }
    });

    axum::serve(listener, build_router(state))
        .with_graceful_shutdown(async {
            let _ = shutdown_rx.await;
        })
        .await?;
    Ok(())
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ReadyHandshake {
    protocol_version: u32,
    engine_version: &'static str,
    lancedb_version: &'static str,
    listen_address: String,
}

async fn authorize(
    State(state): State<AppState>,
    request: Request<axum::body::Body>,
    next: Next,
) -> Result<Response, EngineError> {
    let supplied = request
        .headers()
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .unwrap_or_default();
    if !constant_time_eq(supplied.as_bytes(), state.token.as_bytes()) {
        return Err(EngineError {
            status: StatusCode::UNAUTHORIZED,
            code: "invalid_request",
            message: "missing or invalid bearer token".to_owned(),
        });
    }
    if state.shutting_down.load(Ordering::SeqCst) && request.uri().path() != "/v1/shutdown" {
        return Err(EngineError::busy("engine is shutting down"));
    }
    Ok(next.run(request).await)
}

fn constant_time_eq(left: &[u8], right: &[u8]) -> bool {
    let mut difference = left.len() ^ right.len();
    let max_len = left.len().max(right.len());
    for index in 0..max_len {
        difference |= left.get(index).copied().unwrap_or_default() as usize
            ^ right.get(index).copied().unwrap_or_default() as usize;
    }
    difference == 0
}

async fn health(State(state): State<AppState>) -> Json<HealthResponse> {
    Json(HealthResponse {
        protocol_version: PROTOCOL_VERSION,
        engine_version: ENGINE_VERSION,
        lancedb_version: LANCEDB_VERSION,
        status: if state.shutting_down.load(Ordering::SeqCst) {
            "shutting_down"
        } else {
            "ok"
        },
        registered_generations: state.engine.registered_generation_count().await,
    })
}

async fn create_generation(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<CreateGenerationResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.create_generation(request).await?))
}

async fn release_generation(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<ReleaseGenerationResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.release_generation(request).await?))
}

async fn import_chunks(
    State(state): State<AppState>,
    headers: HeaderMap,
    body: Bytes,
) -> EngineResult<Json<ImportResponse>> {
    let request = if is_arrow(&headers) {
        ImportRequest {
            base: base_from_headers(&headers)?,
            chunks: chunks_from_arrow_ipc(&body)?,
        }
    } else {
        parse_json(&body)?
    };
    Ok(Json(state.engine.import_chunks(request).await?))
}

async fn replace_file(
    State(state): State<AppState>,
    headers: HeaderMap,
    body: Bytes,
) -> EngineResult<Json<MutationResponse>> {
    let request = if is_arrow(&headers) {
        ReplaceFileRequest {
            base: base_from_headers(&headers)?,
            file_id: required_header(&headers, "x-kbase-file-id")?,
            chunks: chunks_from_arrow_ipc(&body)?,
        }
    } else {
        parse_json(&body)?
    };
    Ok(Json(state.engine.replace_file(request).await?))
}

async fn delete_file(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<MutationResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.delete_file(request).await?))
}

async fn search(State(state): State<AppState>, body: Bytes) -> EngineResult<Json<SearchResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.search(request).await?))
}

async fn read_chunk(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<ReadChunkResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.read_chunk(request).await?))
}

async fn read_path(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<ReadPathResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.read_path(request).await?))
}

async fn build_indexes(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<BuildIndexesResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.build_indexes(request).await?))
}

async fn validate_generation(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<ValidateResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.validate(request).await?))
}

async fn stats(State(state): State<AppState>, body: Bytes) -> EngineResult<Json<StatsResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.stats(request).await?))
}

async fn optimize(
    State(state): State<AppState>,
    body: Bytes,
) -> EngineResult<Json<OptimizeResponse>> {
    let request = parse_json(&body)?;
    Ok(Json(state.engine.optimize(request).await?))
}

async fn shutdown(State(state): State<AppState>) -> impl IntoResponse {
    state.request_shutdown().await;
    Json(serde_json::json!({"status": "shutting_down"}))
}

fn is_arrow(headers: &HeaderMap) -> bool {
    headers
        .get(header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| {
            let value = value.to_ascii_lowercase();
            value.starts_with("application/vnd.apache.arrow.stream")
                || value.starts_with("application/x-arrow")
        })
}

fn parse_json<T: serde::de::DeserializeOwned>(body: &[u8]) -> EngineResult<T> {
    serde_json::from_slice(body)
        .map_err(|error| EngineError::invalid(format!("invalid JSON body: {error}")))
}

fn base_from_headers(headers: &HeaderMap) -> EngineResult<BaseRequest> {
    Ok(BaseRequest {
        request_id: required_header(headers, "x-kbase-request-id")?,
        agent_key: required_header(headers, "x-kbase-agent-key")?,
        generation_id: required_header(headers, "x-kbase-generation-id")?,
    })
}

fn required_header(headers: &HeaderMap, name: &'static str) -> EngineResult<String> {
    headers
        .get(name)
        .ok_or_else(|| EngineError::invalid(format!("missing {name} header")))?
        .to_str()
        .map(str::to_owned)
        .map_err(|_| EngineError::invalid(format!("{name} header is not valid UTF-8")))
}

fn spawn_parent_watchdog(state: AppState, parent_pid: u32) {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(1));
        interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        loop {
            interval.tick().await;
            if !parent_process_alive(parent_pid) {
                tracing::warn!(parent_pid, "parent process exited; shutting down sidecar");
                state.request_shutdown().await;
                return;
            }
            if state.shutting_down.load(Ordering::SeqCst) {
                return;
            }
        }
    });
}

#[cfg(unix)]
fn parent_process_alive(pid: u32) -> bool {
    let result = unsafe { libc::kill(pid as libc::pid_t, 0) };
    result == 0 || std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

#[cfg(windows)]
fn parent_process_alive(pid: u32) -> bool {
    use windows_sys::Win32::{
        Foundation::{CloseHandle, WAIT_TIMEOUT},
        System::Threading::{OpenProcess, SYNCHRONIZE, WaitForSingleObject},
    };
    let handle = unsafe { OpenProcess(SYNCHRONIZE, 0, pid) };
    if handle.is_null() {
        return false;
    }
    let status = unsafe { WaitForSingleObject(handle, 0) };
    unsafe {
        CloseHandle(handle);
    }
    status == WAIT_TIMEOUT
}

#[cfg(not(any(unix, windows)))]
fn parent_process_alive(_pid: u32) -> bool {
    true
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::{Body, to_bytes};
    use tower::ServiceExt;

    #[test]
    fn token_comparison_checks_length_and_content() {
        assert!(constant_time_eq(b"secret", b"secret"));
        assert!(!constant_time_eq(b"secret", b"secrex"));
        assert!(!constant_time_eq(b"secret", b"secret-longer"));
    }

    #[tokio::test]
    async fn malformed_json_uses_stable_error_envelope() {
        let temp = tempfile::tempdir().unwrap();
        let (shutdown_tx, _shutdown_rx) = oneshot::channel();
        let token = "0123456789abcdef0123456789abcdef";
        let state = AppState::new(
            token.to_owned(),
            vec![temp.path().to_path_buf()],
            shutdown_tx,
        )
        .unwrap();
        let response = build_router(state)
            .oneshot(
                Request::post("/v1/search")
                    .header(header::AUTHORIZATION, format!("Bearer {token}"))
                    .body(Body::from("{"))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
        let body = to_bytes(response.into_body(), 1024).await.unwrap();
        let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(payload["error"]["code"], "invalid_request");
    }
}
