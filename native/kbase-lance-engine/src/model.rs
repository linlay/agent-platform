use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

fn default_fts_tokenizer() -> String {
    "icu".to_owned()
}

fn default_limit() -> usize {
    8
}

fn default_read_offset() -> i32 {
    1
}

fn default_read_limit() -> i32 {
    200
}

fn default_rrf_k() -> usize {
    60
}

fn default_vector_weight() -> f32 {
    0.7
}

fn default_fts_weight() -> f32 {
    0.3
}

fn default_candidate_floor() -> usize {
    30
}

fn default_candidate_multiplier() -> usize {
    4
}

fn default_candidate_max() -> usize {
    500
}

fn default_ann_min_rows() -> usize {
    50_000
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BaseRequest {
    pub request_id: String,
    pub agent_key: String,
    pub generation_id: String,
}

impl BaseRequest {
    pub fn validate(&self) -> Result<(), String> {
        if self.request_id.trim().is_empty() {
            return Err("requestId is required".to_owned());
        }
        if self.agent_key.trim().is_empty() {
            return Err("agentKey is required".to_owned());
        }
        validate_generation_id(&self.generation_id)
    }
}

pub fn validate_generation_id(value: &str) -> Result<(), String> {
    if value.is_empty() || value.len() > 128 {
        return Err("generationId must contain 1..128 characters".to_owned());
    }
    if !value
        .bytes()
        .all(|ch| ch.is_ascii_alphanumeric() || matches!(ch, b'-' | b'_'))
    {
        return Err("generationId may only contain ASCII letters, digits, '-' and '_'".to_owned());
    }
    Ok(())
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CreateGenerationRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    pub storage_dir: String,
    pub vector_dimension: usize,
    #[serde(default)]
    pub embedding_model: String,
    #[serde(default = "default_fts_tokenizer")]
    pub fts_tokenizer: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CreateGenerationResponse {
    pub request_id: String,
    pub generation_id: String,
    pub table_version: u64,
    pub created: bool,
    pub vector_dimension: usize,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReleaseGenerationRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ReleaseGenerationResponse {
    pub request_id: String,
    pub generation_id: String,
    pub released: bool,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", default)]
pub struct ChunkWire {
    pub chunk_id: String,
    pub file_id: String,
    pub path: String,
    pub ext: String,
    pub ordinal: i32,
    pub heading: String,
    pub start_line: i32,
    pub end_line: i32,
    pub page_start: i32,
    pub page_end: i32,
    pub slide_start: i32,
    pub slide_end: i32,
    pub source_type: String,
    pub locator_json: String,
    pub content: String,
    pub fts_text: String,
    pub content_hash: String,
    pub embedding_model: String,
    pub embedding_dimension: i32,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub vector: Vec<f32>,
    pub updated_at: i64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ImportRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    pub chunks: Vec<ChunkWire>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReplaceFileRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    pub file_id: String,
    pub chunks: Vec<ChunkWire>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DeleteFileRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    pub file_id: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ImportResponse {
    pub request_id: String,
    pub generation_id: String,
    pub table_version: u64,
    pub imported_rows: u64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MutationResponse {
    pub request_id: String,
    pub generation_id: String,
    pub table_version: u64,
    pub inserted_rows: u64,
    pub updated_rows: u64,
    pub deleted_rows: u64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SearchRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    #[serde(default)]
    pub query: String,
    #[serde(default)]
    pub vector: Vec<f32>,
    #[serde(default)]
    pub offset: usize,
    #[serde(default = "default_limit")]
    pub limit: usize,
    #[serde(default = "default_rrf_k")]
    pub rrf_k: usize,
    #[serde(default = "default_vector_weight")]
    pub vector_weight: f32,
    #[serde(default = "default_fts_weight")]
    pub fts_weight: f32,
    #[serde(default = "default_candidate_floor")]
    pub candidate_floor: usize,
    #[serde(default = "default_candidate_multiplier")]
    pub candidate_multiplier: usize,
    #[serde(default = "default_candidate_max")]
    pub candidate_max: usize,
    #[serde(default)]
    pub path_prefix: String,
    #[serde(default)]
    pub path_glob: String,
    #[serde(default, rename = "type")]
    pub file_type: String,
}

impl SearchRequest {
    pub fn validate(&self, dimension: usize) -> Result<(), String> {
        self.base.validate()?;
        if self.query.trim().is_empty() && self.vector.is_empty() {
            return Err("query or vector is required".to_owned());
        }
        if !self.vector.is_empty() && self.vector.len() != dimension {
            return Err(format!(
                "query vector dimension {} does not match generation dimension {}",
                self.vector.len(),
                dimension
            ));
        }
        if self.vector.iter().any(|value| !value.is_finite()) {
            return Err("query vector contains NaN or infinity".to_owned());
        }
        if !(1..=50).contains(&self.limit) {
            return Err("limit must be between 1 and 50".to_owned());
        }
        if !(1..=1000).contains(&self.rrf_k) {
            return Err("rrfK must be between 1 and 1000".to_owned());
        }
        if self.vector_weight < 0.0
            || self.fts_weight < 0.0
            || !self.vector_weight.is_finite()
            || !self.fts_weight.is_finite()
            || self.vector_weight + self.fts_weight <= 0.0
        {
            return Err(
                "retrieval weights must be finite, non-negative and not both zero".to_owned(),
            );
        }
        if self.candidate_floor < self.limit {
            return Err("candidateFloor must be at least limit".to_owned());
        }
        if self.candidate_multiplier == 0 {
            return Err("candidateMultiplier must be positive".to_owned());
        }
        if self.candidate_max < self.candidate_floor || self.candidate_max > 2000 {
            return Err("candidateMax must be between candidateFloor and 2000".to_owned());
        }
        Ok(())
    }

    pub fn candidate_k(&self) -> usize {
        self.candidate_floor
            .max(
                self.candidate_multiplier
                    .saturating_mul(self.offset.saturating_add(self.limit)),
            )
            .min(self.candidate_max)
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SearchMatch {
    pub chunk: ChunkWire,
    pub score: f32,
    pub match_type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub vector_rank: Option<usize>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub fts_rank: Option<usize>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SearchResponse {
    pub request_id: String,
    pub generation_id: String,
    pub matches: Vec<SearchMatch>,
    pub count: usize,
    pub match_count: usize,
    pub truncated: bool,
    pub vector_candidates: usize,
    pub fts_candidates: usize,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReadChunkRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    pub chunk_id: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ReadChunkResponse {
    pub request_id: String,
    pub generation_id: String,
    pub chunk: Option<ChunkWire>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReadPathRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    pub path: String,
    #[serde(default = "default_read_offset")]
    pub offset: i32,
    #[serde(default = "default_read_limit")]
    pub limit: i32,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ReadPathResponse {
    pub request_id: String,
    pub generation_id: String,
    pub chunks: Vec<ChunkWire>,
    pub count: usize,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BuildIndexesRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    #[serde(default = "default_ann_min_rows")]
    pub ann_min_rows: usize,
    #[serde(default)]
    pub fts_tokenizer: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BuildIndexesResponse {
    pub request_id: String,
    pub generation_id: String,
    pub table_version: u64,
    pub row_count: usize,
    pub vector_index_type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ann_recall_at_10: Option<f32>,
    pub indexes: Vec<IndexInfo>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ValidateRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    #[serde(default)]
    pub expected_chunk_count: Option<usize>,
    #[serde(default)]
    pub expected_file_count: Option<usize>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ValidateResponse {
    pub request_id: String,
    pub generation_id: String,
    pub valid: bool,
    pub row_count: usize,
    pub file_count: usize,
    pub duplicate_chunk_ids: usize,
    pub invalid_vectors: usize,
    pub chunk_id_digest: String,
    pub file_id_digest: String,
    pub file_chunk_hashes: BTreeMap<String, String>,
    pub table_version: u64,
    pub indexes: Vec<IndexInfo>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct StatsRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StatsResponse {
    pub request_id: String,
    pub generation_id: String,
    pub row_count: usize,
    pub file_count: usize,
    pub table_version: u64,
    pub vector_dimension: usize,
    pub indexes: Vec<IndexInfo>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct IndexInfo {
    pub name: String,
    pub index_type: String,
    pub columns: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indexed_rows: Option<usize>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unindexed_rows: Option<usize>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OptimizeRequest {
    #[serde(flatten)]
    pub base: BaseRequest,
    #[serde(default)]
    pub retention_hours: Option<i64>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct OptimizeResponse {
    pub request_id: String,
    pub generation_id: String,
    pub table_version: u64,
    pub optimized: bool,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HealthResponse {
    pub protocol_version: u32,
    pub engine_version: &'static str,
    pub lancedb_version: &'static str,
    pub status: &'static str,
    pub registered_generations: usize,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn candidate_k_is_bounded() {
        let req: SearchRequest = serde_json::from_value(serde_json::json!({
            "requestId": "r",
            "agentKey": "a",
            "generationId": "g",
            "query": "x",
            "offset": 200,
            "limit": 50
        }))
        .unwrap();
        assert_eq!(req.candidate_k(), 500);
    }

    #[test]
    fn generation_id_rejects_paths() {
        assert!(validate_generation_id("../escape").is_err());
        assert!(validate_generation_id("good_generation-1").is_ok());
    }
}
