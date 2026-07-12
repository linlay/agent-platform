use axum::{
    Json,
    http::StatusCode,
    response::{IntoResponse, Response},
};
use serde::Serialize;

pub type EngineResult<T> = Result<T, EngineError>;

#[derive(Debug, thiserror::Error)]
#[error("{message}")]
pub struct EngineError {
    pub status: StatusCode,
    pub code: &'static str,
    pub message: String,
}

impl EngineError {
    pub fn invalid(message: impl Into<String>) -> Self {
        Self::new(StatusCode::BAD_REQUEST, "invalid_request", message)
    }

    pub fn generation_not_found(message: impl Into<String>) -> Self {
        Self::new(StatusCode::NOT_FOUND, "generation_not_found", message)
    }

    pub fn schema(message: impl Into<String>) -> Self {
        Self::new(StatusCode::CONFLICT, "schema_mismatch", message)
    }

    pub fn dimension(message: impl Into<String>) -> Self {
        Self::new(
            StatusCode::UNPROCESSABLE_ENTITY,
            "dimension_mismatch",
            message,
        )
    }

    pub fn index_not_ready(message: impl Into<String>) -> Self {
        Self::new(StatusCode::CONFLICT, "index_not_ready", message)
    }

    pub fn query(message: impl Into<String>) -> Self {
        Self::new(StatusCode::BAD_REQUEST, "query_invalid", message)
    }

    pub fn busy(message: impl Into<String>) -> Self {
        Self::new(StatusCode::CONFLICT, "storage_busy", message)
    }

    pub fn corrupt(message: impl Into<String>) -> Self {
        Self::new(
            StatusCode::INTERNAL_SERVER_ERROR,
            "storage_corrupt",
            message,
        )
    }

    pub fn internal(message: impl Into<String>) -> Self {
        Self::new(
            StatusCode::INTERNAL_SERVER_ERROR,
            "engine_internal",
            message,
        )
    }

    fn new(status: StatusCode, code: &'static str, message: impl Into<String>) -> Self {
        Self {
            status,
            code,
            message: message.into(),
        }
    }

    pub fn from_lance(error: lancedb::Error) -> Self {
        let message = error.to_string();
        let lower = message.to_ascii_lowercase();
        if lower.contains("dimension mismatch")
            || lower.contains("vector dimension")
            || lower.contains("expected dimension")
            || lower.contains("fixed size list")
        {
            Self::dimension(message)
        } else if lower.contains("schema") {
            Self::schema(message)
        } else if lower.contains("parse") || lower.contains("query syntax") {
            Self::query(message)
        } else if lower.contains("lock") || lower.contains("conflict") || lower.contains("busy") {
            Self::busy(message)
        } else if lower.contains("corrupt") || lower.contains("invalid manifest") {
            Self::corrupt(message)
        } else {
            Self::internal(message)
        }
    }
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ErrorEnvelope<'a> {
    error: ErrorBody<'a>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ErrorBody<'a> {
    code: &'a str,
    message: &'a str,
}

impl IntoResponse for EngineError {
    fn into_response(self) -> Response {
        let payload = ErrorEnvelope {
            error: ErrorBody {
                code: self.code,
                message: &self.message,
            },
        };
        (self.status, Json(payload)).into_response()
    }
}

impl From<arrow_schema::ArrowError> for EngineError {
    fn from(value: arrow_schema::ArrowError) -> Self {
        Self::schema(value.to_string())
    }
}

impl From<std::io::Error> for EngineError {
    fn from(value: std::io::Error) -> Self {
        Self::internal(value.to_string())
    }
}
