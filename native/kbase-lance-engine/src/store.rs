use std::{
    collections::{BTreeMap, HashMap, HashSet},
    path::{Path, PathBuf},
    sync::Arc,
};

use arrow_array::{
    Array, FixedSizeListArray, Float32Array, Int32Array, Int64Array, RecordBatch,
    RecordBatchIterator, StringArray, types::Float32Type,
};
use arrow_schema::{DataType, Field, Schema, SchemaRef};
use futures::TryStreamExt;
use globset::{GlobBuilder, GlobMatcher};
use icu_segmenter::WordSegmenter;
use lancedb::{
    DistanceType, Table, connect,
    index::{
        Index,
        scalar::{BTreeIndexBuilder, BitmapIndexBuilder, FtsIndexBuilder, FullTextSearchQuery},
        vector::IvfHnswSqIndexBuilder,
    },
    query::{ExecutableQuery, QueryBase, Select},
    table::{CompactionOptions, OptimizeAction, OptimizeOptions},
};
use sha2::{Digest, Sha256};
use tokio::sync::{Mutex, RwLock};

use crate::{
    error::{EngineError, EngineResult},
    model::*,
};

const TABLE_NAME: &str = "chunks";
const ANN_RECALL_SAMPLE_COUNT: usize = 32;
const ANN_NPROBES: usize = 32;
const ANN_REFINE_FACTOR: u32 = 3;
const CHUNK_COLUMNS: &[&str] = &[
    "chunk_id",
    "file_id",
    "path",
    "ext",
    "ordinal",
    "heading",
    "start_line",
    "end_line",
    "page_start",
    "page_end",
    "slide_start",
    "slide_end",
    "source_type",
    "locator_json",
    "content",
    "fts_text",
    "content_hash",
    "embedding_model",
    "embedding_dimension",
    "updated_at",
];

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct GenerationKey {
    agent_key: String,
    generation_id: String,
}

impl GenerationKey {
    fn new(base: &BaseRequest) -> Self {
        Self {
            agent_key: base.agent_key.clone(),
            generation_id: base.generation_id.clone(),
        }
    }
}

struct GenerationHandle {
    vector_dimension: usize,
    embedding_model: String,
    fts_tokenizer: String,
    table: Table,
    write_lock: Mutex<()>,
}

#[derive(Clone)]
pub struct Engine {
    generations: Arc<RwLock<HashMap<GenerationKey, Arc<GenerationHandle>>>>,
    allowed_roots: Arc<Vec<PathBuf>>,
}

impl Engine {
    pub fn new(allowed_roots: Vec<PathBuf>) -> EngineResult<Self> {
        let mut canonical = Vec::with_capacity(allowed_roots.len());
        for root in allowed_roots {
            let root = root
                .canonicalize()
                .map_err(|error| EngineError::invalid(format!("invalid allowed root: {error}")))?;
            if !root.is_dir() {
                return Err(EngineError::invalid(format!(
                    "allowed root is not a directory: {}",
                    root.display()
                )));
            }
            canonical.push(root);
        }
        Ok(Self {
            generations: Arc::new(RwLock::new(HashMap::new())),
            allowed_roots: Arc::new(canonical),
        })
    }

    pub async fn registered_generation_count(&self) -> usize {
        self.generations.read().await.len()
    }

    pub async fn release_generation(
        &self,
        request: ReleaseGenerationRequest,
    ) -> EngineResult<ReleaseGenerationResponse> {
        request.base.validate().map_err(EngineError::invalid)?;
        let released = self
            .generations
            .write()
            .await
            .remove(&GenerationKey::new(&request.base))
            .is_some();
        Ok(ReleaseGenerationResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            released,
        })
    }

    pub async fn create_generation(
        &self,
        request: CreateGenerationRequest,
    ) -> EngineResult<CreateGenerationResponse> {
        request.base.validate().map_err(EngineError::invalid)?;
        if request.vector_dimension == 0 || request.vector_dimension > i32::MAX as usize {
            return Err(EngineError::dimension(
                "vectorDimension must be between 1 and 2147483647",
            ));
        }
        validate_tokenizer(&request.fts_tokenizer)?;

        let storage_dir = canonical_storage_dir(&request.storage_dir, &self.allowed_roots)?;
        let database_dir = storage_dir
            .join("generations")
            .join(&request.base.generation_id)
            .join("lance");
        tokio::fs::create_dir_all(&database_dir).await?;
        let database_dir = database_dir.canonicalize()?;
        ensure_under_allowed_roots(&database_dir, &self.allowed_roots)?;

        let database_uri = database_dir
            .to_str()
            .ok_or_else(|| EngineError::invalid("storageDir is not valid UTF-8"))?;
        let database = connect(database_uri)
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        let table_names = database
            .table_names()
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        let created = !table_names.iter().any(|name| name == TABLE_NAME);
        let table = if created {
            database
                .create_empty_table(TABLE_NAME, chunk_schema(request.vector_dimension))
                .execute()
                .await
                .map_err(EngineError::from_lance)?
        } else {
            database
                .open_table(TABLE_NAME)
                .execute()
                .await
                .map_err(EngineError::from_lance)?
        };
        let table_schema = table.schema().await.map_err(EngineError::from_lance)?;
        let actual_dimension = schema_dimension(table_schema.as_ref())?;
        if actual_dimension != request.vector_dimension {
            return Err(EngineError::dimension(format!(
                "existing table dimension {actual_dimension} does not match requested dimension {}",
                request.vector_dimension
            )));
        }
        validate_chunk_schema(table_schema.as_ref(), request.vector_dimension)?;

        let table_version = table.version().await.map_err(EngineError::from_lance)?;
        let handle = Arc::new(GenerationHandle {
            vector_dimension: request.vector_dimension,
            embedding_model: request.embedding_model,
            fts_tokenizer: request.fts_tokenizer.to_ascii_lowercase(),
            table,
            write_lock: Mutex::new(()),
        });
        self.generations
            .write()
            .await
            .insert(GenerationKey::new(&request.base), handle);

        Ok(CreateGenerationResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            table_version,
            created,
            vector_dimension: actual_dimension,
        })
    }

    async fn generation(&self, base: &BaseRequest) -> EngineResult<Arc<GenerationHandle>> {
        base.validate().map_err(EngineError::invalid)?;
        self.generations
            .read()
            .await
            .get(&GenerationKey::new(base))
            .cloned()
            .ok_or_else(|| {
                EngineError::generation_not_found(format!(
                    "generation {} is not registered for agent {}",
                    base.generation_id, base.agent_key
                ))
            })
    }

    pub async fn import_chunks(&self, mut request: ImportRequest) -> EngineResult<ImportResponse> {
        let generation = self.generation(&request.base).await?;
        prepare_chunks(&generation, &mut request.chunks)?;
        let imported_rows = request.chunks.len() as u64;
        let _guard = generation.write_lock.lock().await;

        let table_version = if request.chunks.is_empty() {
            generation
                .table
                .version()
                .await
                .map_err(EngineError::from_lance)?
        } else {
            let reader = chunk_reader(&request.chunks, generation.vector_dimension)?;
            let mut merge = generation.table.merge_insert(&["chunk_id"]);
            merge
                .when_matched_update_all(None)
                .when_not_matched_insert_all();
            merge
                .execute(reader)
                .await
                .map_err(EngineError::from_lance)?
                .version
        };

        Ok(ImportResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            table_version,
            imported_rows,
        })
    }

    pub async fn replace_file(
        &self,
        mut request: ReplaceFileRequest,
    ) -> EngineResult<MutationResponse> {
        if request.file_id.trim().is_empty() {
            return Err(EngineError::invalid("fileId is required"));
        }
        let generation = self.generation(&request.base).await?;
        for chunk in &request.chunks {
            if chunk.file_id != request.file_id {
                return Err(EngineError::invalid(format!(
                    "chunk {} belongs to fileId {}, expected {}",
                    chunk.chunk_id, chunk.file_id, request.file_id
                )));
            }
        }
        prepare_chunks(&generation, &mut request.chunks)?;
        let _guard = generation.write_lock.lock().await;

        if request.chunks.is_empty() {
            let predicate = format!("file_id = '{}'", sql_literal(&request.file_id));
            let result = generation
                .table
                .delete(&predicate)
                .await
                .map_err(EngineError::from_lance)?;
            return Ok(MutationResponse {
                request_id: request.base.request_id,
                generation_id: request.base.generation_id,
                table_version: result.version,
                inserted_rows: 0,
                updated_rows: 0,
                deleted_rows: result.num_deleted_rows,
            });
        }

        let reader = chunk_reader(&request.chunks, generation.vector_dimension)?;
        let mut merge = generation.table.merge_insert(&["chunk_id"]);
        merge
            .when_matched_update_all(None)
            .when_not_matched_insert_all()
            .when_not_matched_by_source_delete(Some(format!(
                "file_id = '{}'",
                sql_literal(&request.file_id)
            )));
        let result = merge
            .execute(reader)
            .await
            .map_err(EngineError::from_lance)?;
        Ok(MutationResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            table_version: result.version,
            inserted_rows: result.num_inserted_rows,
            updated_rows: result.num_updated_rows,
            deleted_rows: result.num_deleted_rows,
        })
    }

    pub async fn delete_file(&self, request: DeleteFileRequest) -> EngineResult<MutationResponse> {
        if request.file_id.trim().is_empty() {
            return Err(EngineError::invalid("fileId is required"));
        }
        let generation = self.generation(&request.base).await?;
        let _guard = generation.write_lock.lock().await;
        let predicate = format!("file_id = '{}'", sql_literal(&request.file_id));
        let result = generation
            .table
            .delete(&predicate)
            .await
            .map_err(EngineError::from_lance)?;
        Ok(MutationResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            table_version: result.version,
            inserted_rows: 0,
            updated_rows: 0,
            deleted_rows: result.num_deleted_rows,
        })
    }

    pub async fn search(&self, request: SearchRequest) -> EngineResult<SearchResponse> {
        let generation = self.generation(&request.base).await?;
        request
            .validate(generation.vector_dimension)
            .map_err(|message| {
                if message.contains("dimension") {
                    EngineError::dimension(message)
                } else {
                    EngineError::query(message)
                }
            })?;
        let candidate_k = request.candidate_k();
        let fetch_k = if request.path_glob.is_empty() {
            candidate_k
        } else {
            request.candidate_max
        };
        let filter = search_filter(&request);
        let glob_matcher = if request.path_glob.is_empty() {
            None
        } else {
            Some(compile_path_glob(&request.path_glob)?)
        };

        let vector_future = async {
            if request.vector.is_empty() || request.vector_weight == 0.0 {
                Ok((Vec::new(), false))
            } else {
                vector_candidates(
                    &generation,
                    &request.vector,
                    fetch_k,
                    &filter,
                    glob_matcher.as_ref(),
                )
                .await
            }
        };
        let fts_future = async {
            if request.query.trim().is_empty() || request.fts_weight == 0.0 {
                Ok((Vec::new(), false))
            } else {
                fts_candidates(
                    &generation,
                    &request.query,
                    fetch_k,
                    &filter,
                    glob_matcher.as_ref(),
                )
                .await
            }
        };
        let ((mut vector, vector_fetch_capped), (mut fts, fts_fetch_capped)) =
            tokio::try_join!(vector_future, fts_future)?;
        vector.truncate(candidate_k);
        fts.truncate(candidate_k);

        let vector_candidates = vector.len();
        let fts_candidates = fts.len();
        let mut merged: HashMap<String, SearchMatch> = HashMap::new();
        for (index, chunk) in vector.into_iter().enumerate() {
            merged.insert(
                chunk.chunk_id.clone(),
                SearchMatch {
                    chunk,
                    score: 0.0,
                    match_type: "vector".to_owned(),
                    vector_rank: Some(index + 1),
                    fts_rank: None,
                },
            );
        }
        for (index, chunk) in fts.into_iter().enumerate() {
            let rank = index + 1;
            if let Some(hit) = merged.get_mut(&chunk.chunk_id) {
                hit.fts_rank = Some(rank);
                hit.match_type = "hybrid".to_owned();
            } else {
                merged.insert(
                    chunk.chunk_id.clone(),
                    SearchMatch {
                        chunk,
                        score: 0.0,
                        match_type: "fts".to_owned(),
                        vector_rank: None,
                        fts_rank: Some(rank),
                    },
                );
            }
        }

        let weight_sum = request.vector_weight + request.fts_weight;
        let vector_weight = request.vector_weight / weight_sum;
        let fts_weight = request.fts_weight / weight_sum;
        let normalizer = (request.rrf_k + 1) as f32;
        let mut matches = merged.into_values().collect::<Vec<_>>();
        for hit in &mut matches {
            let vector_score = hit
                .vector_rank
                .map(|rank| vector_weight / (request.rrf_k + rank) as f32)
                .unwrap_or_default();
            let fts_score = hit
                .fts_rank
                .map(|rank| fts_weight / (request.rrf_k + rank) as f32)
                .unwrap_or_default();
            hit.score = ((vector_score + fts_score) * normalizer).clamp(0.0, 1.0);
        }
        matches.sort_by(|left, right| {
            right
                .score
                .total_cmp(&left.score)
                .then_with(|| left.chunk.path.cmp(&right.chunk.path))
                .then_with(|| left.chunk.start_line.cmp(&right.chunk.start_line))
                .then_with(|| left.chunk.chunk_id.cmp(&right.chunk.chunk_id))
        });

        let match_count = matches.len();
        let truncated = search_is_truncated(
            vector_candidates,
            fts_candidates,
            request.candidate_max,
            glob_matcher.is_some(),
            vector_fetch_capped,
            fts_fetch_capped,
            request.offset,
            request.limit,
            match_count,
        );
        let matches = matches
            .into_iter()
            .skip(request.offset)
            .take(request.limit)
            .collect::<Vec<_>>();

        Ok(SearchResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            count: matches.len(),
            matches,
            match_count,
            truncated,
            vector_candidates,
            fts_candidates,
        })
    }

    pub async fn read_chunk(&self, request: ReadChunkRequest) -> EngineResult<ReadChunkResponse> {
        if request.chunk_id.trim().is_empty() {
            return Err(EngineError::invalid("chunkId is required"));
        }
        let generation = self.generation(&request.base).await?;
        let stream = generation
            .table
            .query()
            .only_if(format!("chunk_id = '{}'", sql_literal(&request.chunk_id)))
            .limit(1)
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        let mut chunks = collect_chunks(stream, true).await?;
        Ok(ReadChunkResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            chunk: chunks.pop(),
        })
    }

    pub async fn read_path(&self, request: ReadPathRequest) -> EngineResult<ReadPathResponse> {
        if request.path.trim().is_empty() {
            return Err(EngineError::invalid("path is required"));
        }
        if request.limit > 10_000 {
            return Err(EngineError::invalid("limit must not exceed 10000 lines"));
        }
        let generation = self.generation(&request.base).await?;
        let stream = generation
            .table
            .query()
            .only_if(format!("path = '{}'", sql_literal(&request.path)))
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        let mut chunks = collect_chunks(stream, false).await?;
        chunks.sort_by(|left, right| {
            left.ordinal
                .cmp(&right.ordinal)
                .then_with(|| left.chunk_id.cmp(&right.chunk_id))
        });
        let chunks = select_path_chunks(chunks, request.offset, request.limit);
        Ok(ReadPathResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            count: chunks.len(),
            chunks,
        })
    }

    pub async fn build_indexes(
        &self,
        request: BuildIndexesRequest,
    ) -> EngineResult<BuildIndexesResponse> {
        if request.ann_min_rows < 1000 {
            return Err(EngineError::invalid("annMinRows must be at least 1000"));
        }
        let generation = self.generation(&request.base).await?;
        if !request.fts_tokenizer.is_empty()
            && !request
                .fts_tokenizer
                .eq_ignore_ascii_case(&generation.fts_tokenizer)
        {
            return Err(EngineError::schema(format!(
                "generation was written for {} tokenizer, cannot build {} tokenizer without rebuilding",
                generation.fts_tokenizer, request.fts_tokenizer
            )));
        }
        let _guard = generation.write_lock.lock().await;
        let row_count = generation
            .table
            .count_rows(None)
            .await
            .map_err(EngineError::from_lance)?;

        let fts = fts_index_builder(&generation.fts_tokenizer)?;
        generation
            .table
            .create_index(&["fts_text"], Index::FTS(fts))
            .name("fts_text_fts".to_owned())
            .replace(true)
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        for (column, name) in [
            ("chunk_id", "chunk_id_btree"),
            ("file_id", "file_id_btree"),
            ("path", "path_btree"),
        ] {
            generation
                .table
                .create_index(&[column], Index::BTree(BTreeIndexBuilder::default()))
                .name(name.to_owned())
                .replace(true)
                .execute()
                .await
                .map_err(EngineError::from_lance)?;
        }
        generation
            .table
            .create_index(&["ext"], Index::Bitmap(BitmapIndexBuilder::default()))
            .name("ext_bitmap".to_owned())
            .replace(true)
            .execute()
            .await
            .map_err(EngineError::from_lance)?;

        let (vector_index_type, ann_recall_at_10) = if row_count >= request.ann_min_rows {
            generation
                .table
                .create_index(
                    &["vector"],
                    Index::IvfHnswSq(
                        IvfHnswSqIndexBuilder::default().distance_type(DistanceType::Cosine),
                    ),
                )
                .name("vector_ivf_hnsw_sq".to_owned())
                .replace(true)
                .execute()
                .await
                .map_err(EngineError::from_lance)?;
            let recall =
                validate_ann_recall(&generation.table, ANN_RECALL_SAMPLE_COUNT, 10).await?;
            if recall < 0.95 {
                generation
                    .table
                    .drop_index("vector_ivf_hnsw_sq")
                    .await
                    .map_err(EngineError::from_lance)?;
                ("flat", Some(recall))
            } else {
                ("IVF_HNSW_SQ", Some(recall))
            }
        } else {
            ("flat", None)
        };

        Ok(BuildIndexesResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            table_version: generation
                .table
                .version()
                .await
                .map_err(EngineError::from_lance)?,
            row_count,
            vector_index_type: vector_index_type.to_owned(),
            ann_recall_at_10,
            indexes: index_info(&generation.table).await?,
        })
    }

    pub async fn validate(&self, request: ValidateRequest) -> EngineResult<ValidateResponse> {
        let generation = self.generation(&request.base).await?;
        let stream = generation
            .table
            .query()
            .select(Select::columns(&[
                "chunk_id",
                "file_id",
                "path",
                "ordinal",
                "heading",
                "start_line",
                "end_line",
                "page_start",
                "page_end",
                "slide_start",
                "slide_end",
                "source_type",
                "locator_json",
                "content_hash",
                "embedding_dimension",
                "vector",
            ]))
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        let batches = stream
            .try_collect::<Vec<_>>()
            .await
            .map_err(EngineError::from_lance)?;
        let mut chunk_ids = HashSet::new();
        let mut files = HashSet::new();
        let mut duplicates = 0usize;
        let mut invalid_vectors = 0usize;
        let mut row_count = 0usize;
        let mut file_chunk_items: HashMap<String, Vec<Vec<u8>>> = HashMap::new();
        for batch in batches {
            for row in 0..batch.num_rows() {
                row_count += 1;
                let chunk_id = string_value(&batch, "chunk_id", row)?;
                if !chunk_ids.insert(chunk_id.clone()) {
                    duplicates += 1;
                }
                let file_id = string_value(&batch, "file_id", row)?;
                files.insert(file_id.clone());
                let declared = int32_value(&batch, "embedding_dimension", row)?;
                let vector = vector_value(&batch, "vector", row)?;
                if declared != generation.vector_dimension as i32
                    || vector.len() != generation.vector_dimension
                    || vector.iter().any(|value| !value.is_finite())
                {
                    invalid_vectors += 1;
                }
                let chunk = ChunkWire {
                    chunk_id,
                    file_id: file_id.clone(),
                    path: string_value(&batch, "path", row)?,
                    ordinal: int32_value(&batch, "ordinal", row)?,
                    heading: string_value(&batch, "heading", row)?,
                    start_line: int32_value(&batch, "start_line", row)?,
                    end_line: int32_value(&batch, "end_line", row)?,
                    page_start: int32_value(&batch, "page_start", row)?,
                    page_end: int32_value(&batch, "page_end", row)?,
                    slide_start: int32_value(&batch, "slide_start", row)?,
                    slide_end: int32_value(&batch, "slide_end", row)?,
                    source_type: string_value(&batch, "source_type", row)?,
                    locator_json: string_value(&batch, "locator_json", row)?,
                    content_hash: string_value(&batch, "content_hash", row)?,
                    ..ChunkWire::default()
                };
                file_chunk_items
                    .entry(file_id)
                    .or_default()
                    .push(chunk_validation_item(&chunk));
            }
        }
        let file_count = files.len();
        let chunk_id_digest = stable_id_digest(&chunk_ids);
        let file_id_digest = stable_id_digest(&files);
        let file_chunk_hashes = file_chunk_items
            .into_iter()
            .map(|(file_id, items)| (file_id, chunk_validation_set_hash(items)))
            .collect::<BTreeMap<_, _>>();
        let valid = duplicates == 0
            && invalid_vectors == 0
            && request
                .expected_chunk_count
                .is_none_or(|expected| expected == row_count)
            && request
                .expected_file_count
                .is_none_or(|expected| expected == file_count);
        Ok(ValidateResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            valid,
            row_count,
            file_count,
            duplicate_chunk_ids: duplicates,
            invalid_vectors,
            chunk_id_digest,
            file_id_digest,
            file_chunk_hashes,
            table_version: generation
                .table
                .version()
                .await
                .map_err(EngineError::from_lance)?,
            indexes: index_info(&generation.table).await?,
        })
    }

    pub async fn stats(&self, request: StatsRequest) -> EngineResult<StatsResponse> {
        let generation = self.generation(&request.base).await?;
        let row_count = generation
            .table
            .count_rows(None)
            .await
            .map_err(EngineError::from_lance)?;
        let stream = generation
            .table
            .query()
            .select(Select::columns(&["file_id"]))
            .execute()
            .await
            .map_err(EngineError::from_lance)?;
        let batches = stream
            .try_collect::<Vec<_>>()
            .await
            .map_err(EngineError::from_lance)?;
        let mut files = HashSet::new();
        for batch in batches {
            for row in 0..batch.num_rows() {
                files.insert(string_value(&batch, "file_id", row)?);
            }
        }
        Ok(StatsResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            row_count,
            file_count: files.len(),
            table_version: generation
                .table
                .version()
                .await
                .map_err(EngineError::from_lance)?,
            vector_dimension: generation.vector_dimension,
            indexes: index_info(&generation.table).await?,
        })
    }

    pub async fn optimize(&self, request: OptimizeRequest) -> EngineResult<OptimizeResponse> {
        let generation = self.generation(&request.base).await?;
        let _guard = generation.write_lock.lock().await;
        generation
            .table
            .optimize(OptimizeAction::Compact {
                options: CompactionOptions::default(),
                remap_options: None,
            })
            .await
            .map_err(EngineError::from_lance)?;
        generation
            .table
            .optimize(OptimizeAction::Index(OptimizeOptions::default()))
            .await
            .map_err(EngineError::from_lance)?;
        let retention_hours = request.retention_hours.unwrap_or(168);
        if retention_hours <= 0 {
            return Err(EngineError::invalid("retentionHours must be positive"));
        }
        generation
            .table
            .optimize(OptimizeAction::Prune {
                older_than: Some(
                    lancedb::table::Duration::try_hours(retention_hours)
                        .ok_or_else(|| EngineError::invalid("retentionHours is out of range"))?,
                ),
                delete_unverified: Some(false),
                error_if_tagged_old_versions: Some(true),
            })
            .await
            .map_err(EngineError::from_lance)?;
        Ok(OptimizeResponse {
            request_id: request.base.request_id,
            generation_id: request.base.generation_id,
            table_version: generation
                .table
                .version()
                .await
                .map_err(EngineError::from_lance)?,
            optimized: true,
        })
    }
}

fn select_path_chunks(chunks: Vec<ChunkWire>, offset: i32, limit: i32) -> Vec<ChunkWire> {
    let offset = offset.max(1);
    let limit = if limit <= 0 { 200 } else { limit };
    let end_line = offset.saturating_add(limit).saturating_sub(1);
    let mut selected = Vec::new();
    for chunk in chunks {
        if chunk.end_line < offset {
            continue;
        }
        let reached_end = chunk.end_line >= end_line;
        selected.push(chunk);
        if reached_end {
            break;
        }
    }
    selected
}

fn canonical_storage_dir(value: &str, allowed_roots: &[PathBuf]) -> EngineResult<PathBuf> {
    let path = Path::new(value);
    if !path.is_absolute() {
        return Err(EngineError::invalid("storageDir must be an absolute path"));
    }
    let canonical = path
        .canonicalize()
        .map_err(|error| EngineError::invalid(format!("storageDir must already exist: {error}")))?;
    if !canonical.is_dir() {
        return Err(EngineError::invalid("storageDir is not a directory"));
    }
    ensure_under_allowed_roots(&canonical, allowed_roots)?;
    Ok(canonical)
}

fn ensure_under_allowed_roots(path: &Path, allowed_roots: &[PathBuf]) -> EngineResult<()> {
    if !allowed_roots.is_empty() && !allowed_roots.iter().any(|root| path.starts_with(root)) {
        return Err(EngineError::invalid(format!(
            "storage path {} is outside KBASE_LANCE_ALLOWED_ROOTS",
            path.display()
        )));
    }
    Ok(())
}

fn chunk_schema(dimension: usize) -> SchemaRef {
    Arc::new(Schema::new(vec![
        Field::new("chunk_id", DataType::Utf8, false),
        Field::new("file_id", DataType::Utf8, false),
        Field::new("path", DataType::Utf8, false),
        Field::new("ext", DataType::Utf8, false),
        Field::new("ordinal", DataType::Int32, false),
        Field::new("heading", DataType::Utf8, false),
        Field::new("start_line", DataType::Int32, false),
        Field::new("end_line", DataType::Int32, false),
        Field::new("page_start", DataType::Int32, false),
        Field::new("page_end", DataType::Int32, false),
        Field::new("slide_start", DataType::Int32, false),
        Field::new("slide_end", DataType::Int32, false),
        Field::new("source_type", DataType::Utf8, false),
        Field::new("locator_json", DataType::Utf8, false),
        Field::new("content", DataType::Utf8, false),
        Field::new("fts_text", DataType::Utf8, false),
        Field::new("content_hash", DataType::Utf8, false),
        Field::new("embedding_model", DataType::Utf8, false),
        Field::new("embedding_dimension", DataType::Int32, false),
        Field::new(
            "vector",
            DataType::FixedSizeList(
                Arc::new(Field::new("item", DataType::Float32, true)),
                dimension as i32,
            ),
            false,
        ),
        Field::new("updated_at", DataType::Int64, false),
    ]))
}

fn schema_dimension(schema: &Schema) -> EngineResult<usize> {
    let field = schema
        .field_with_name("vector")
        .map_err(|_| EngineError::schema("chunks table is missing vector column"))?;
    match field.data_type() {
        DataType::FixedSizeList(item, dimension)
            if item.data_type() == &DataType::Float32 && *dimension > 0 =>
        {
            Ok(*dimension as usize)
        }
        other => Err(EngineError::schema(format!(
            "vector column has unsupported type {other:?}"
        ))),
    }
}

fn validate_chunk_schema(schema: &Schema, dimension: usize) -> EngineResult<()> {
    let expected = chunk_schema(dimension);
    if schema.fields().len() != expected.fields().len() {
        return Err(EngineError::schema(format!(
            "chunks table has {} columns, expected {}",
            schema.fields().len(),
            expected.fields().len()
        )));
    }
    for (actual, expected) in schema.fields().iter().zip(expected.fields()) {
        if actual.name() != expected.name()
            || actual.data_type() != expected.data_type()
            || actual.is_nullable() != expected.is_nullable()
        {
            return Err(EngineError::schema(format!(
                "chunks column {} has type {:?} nullable={}, expected {} {:?} nullable={}",
                actual.name(),
                actual.data_type(),
                actual.is_nullable(),
                expected.name(),
                expected.data_type(),
                expected.is_nullable()
            )));
        }
    }
    Ok(())
}

fn prepare_chunks(generation: &GenerationHandle, chunks: &mut [ChunkWire]) -> EngineResult<()> {
    let mut ids = HashSet::with_capacity(chunks.len());
    for chunk in chunks {
        if chunk.chunk_id.trim().is_empty() || chunk.file_id.trim().is_empty() {
            return Err(EngineError::invalid("chunkId and fileId are required"));
        }
        if !ids.insert(chunk.chunk_id.clone()) {
            return Err(EngineError::invalid(format!(
                "duplicate chunkId {} in request",
                chunk.chunk_id
            )));
        }
        if chunk.path.trim().is_empty() {
            return Err(EngineError::invalid(format!(
                "chunk {} has an empty path",
                chunk.chunk_id
            )));
        }
        if chunk.vector.len() != generation.vector_dimension {
            return Err(EngineError::dimension(format!(
                "chunk {} vector dimension {} does not match generation dimension {}",
                chunk.chunk_id,
                chunk.vector.len(),
                generation.vector_dimension
            )));
        }
        if chunk.vector.iter().any(|value| !value.is_finite()) {
            return Err(EngineError::dimension(format!(
                "chunk {} vector contains NaN or infinity",
                chunk.chunk_id
            )));
        }
        if chunk.embedding_dimension != 0
            && chunk.embedding_dimension != generation.vector_dimension as i32
        {
            return Err(EngineError::dimension(format!(
                "chunk {} declares embedding dimension {}, expected {}",
                chunk.chunk_id, chunk.embedding_dimension, generation.vector_dimension
            )));
        }
        chunk.embedding_dimension = generation.vector_dimension as i32;
        if chunk.embedding_model.is_empty() {
            chunk
                .embedding_model
                .clone_from(&generation.embedding_model);
        }
        chunk.ext = normalize_ext(&chunk.ext);
        if chunk.fts_text.trim().is_empty() {
            chunk.fts_text = format!("{} {} {}", chunk.path, chunk.heading, chunk.content);
        }
        chunk.fts_text = tokenize_for_index(&chunk.fts_text, &generation.fts_tokenizer);
    }
    Ok(())
}

fn chunk_reader(
    chunks: &[ChunkWire],
    dimension: usize,
) -> EngineResult<Box<dyn arrow_array::RecordBatchReader + Send>> {
    let schema = chunk_schema(dimension);
    let strings = |field: fn(&ChunkWire) -> &str| {
        Arc::new(StringArray::from_iter_values(chunks.iter().map(field))) as Arc<dyn Array>
    };
    let i32s = |field: fn(&ChunkWire) -> i32| {
        Arc::new(Int32Array::from_iter_values(chunks.iter().map(field))) as Arc<dyn Array>
    };
    let batch = RecordBatch::try_new(
        schema.clone(),
        vec![
            strings(|chunk| &chunk.chunk_id),
            strings(|chunk| &chunk.file_id),
            strings(|chunk| &chunk.path),
            strings(|chunk| &chunk.ext),
            i32s(|chunk| chunk.ordinal),
            strings(|chunk| &chunk.heading),
            i32s(|chunk| chunk.start_line),
            i32s(|chunk| chunk.end_line),
            i32s(|chunk| chunk.page_start),
            i32s(|chunk| chunk.page_end),
            i32s(|chunk| chunk.slide_start),
            i32s(|chunk| chunk.slide_end),
            strings(|chunk| &chunk.source_type),
            strings(|chunk| &chunk.locator_json),
            strings(|chunk| &chunk.content),
            strings(|chunk| &chunk.fts_text),
            strings(|chunk| &chunk.content_hash),
            strings(|chunk| &chunk.embedding_model),
            i32s(|chunk| chunk.embedding_dimension),
            Arc::new(
                FixedSizeListArray::from_iter_primitive::<Float32Type, _, _>(
                    chunks.iter().map(|chunk| {
                        Some(chunk.vector.iter().copied().map(Some).collect::<Vec<_>>())
                    }),
                    dimension as i32,
                ),
            ),
            Arc::new(Int64Array::from_iter_values(
                chunks.iter().map(|chunk| chunk.updated_at),
            )),
        ],
    )?;
    Ok(Box::new(RecordBatchIterator::new(vec![Ok(batch)], schema)))
}

pub fn chunks_from_arrow_ipc(bytes: &[u8]) -> EngineResult<Vec<ChunkWire>> {
    let reader = arrow_ipc::reader::StreamReader::try_new(std::io::Cursor::new(bytes), None)
        .map_err(|error| EngineError::schema(format!("invalid Arrow IPC stream: {error}")))?;
    let mut chunks = Vec::new();
    for batch in reader {
        let batch = batch
            .map_err(|error| EngineError::schema(format!("invalid Arrow IPC batch: {error}")))?;
        for row in 0..batch.num_rows() {
            chunks.push(chunk_from_batch_row(&batch, row, true)?);
        }
    }
    Ok(chunks)
}

async fn vector_candidates(
    generation: &GenerationHandle,
    vector: &[f32],
    limit: usize,
    filter: &str,
    glob: Option<&GlobMatcher>,
) -> EngineResult<(Vec<ChunkWire>, bool)> {
    let mut select_columns = CHUNK_COLUMNS.to_vec();
    select_columns.push("_distance");
    let mut query = generation
        .table
        .vector_search(vector)
        .map_err(EngineError::from_lance)?
        .distance_type(DistanceType::Cosine)
        .nprobes(ANN_NPROBES)
        .ef(ann_search_ef(limit))
        .refine_factor(ANN_REFINE_FACTOR)
        .select(Select::columns(&select_columns))
        .limit(limit);
    if !filter.is_empty() {
        query = query.only_if(filter);
    }
    let stream = query.execute().await.map_err(EngineError::from_lance)?;
    let mut chunks = collect_chunks(stream, false).await?;
    let fetch_capped = chunks.len() >= limit;
    if let Some(glob) = glob {
        chunks.retain(|chunk| glob.is_match(&chunk.path));
    }
    Ok((chunks, fetch_capped))
}

async fn fts_candidates(
    generation: &GenerationHandle,
    query_text: &str,
    limit: usize,
    filter: &str,
    glob: Option<&GlobMatcher>,
) -> EngineResult<(Vec<ChunkWire>, bool)> {
    let tokenized = tokenize_for_index(query_text, &generation.fts_tokenizer);
    if tokenized.trim().is_empty() {
        return Err(EngineError::query(
            "query does not contain searchable terms",
        ));
    }
    let fts_query = FullTextSearchQuery::new(tokenized.clone())
        .with_column("fts_text".to_owned())
        .map_err(|error| EngineError::query(error.to_string()))?;
    let mut select_columns = CHUNK_COLUMNS.to_vec();
    select_columns.push("_score");
    let mut query = generation
        .table
        .query()
        .full_text_search(fts_query)
        .select(Select::columns(&select_columns))
        .limit(limit);
    if !filter.is_empty() {
        query = query.only_if(filter);
    }
    let stream = match query.execute().await {
        Ok(stream) => stream,
        Err(error) if is_fts_query_error(&error.to_string()) => {
            return substring_candidates(generation, &tokenized, limit, filter, glob).await;
        }
        Err(error) if error.to_string().to_ascii_lowercase().contains("index") => {
            return Err(EngineError::index_not_ready(error.to_string()));
        }
        Err(error) => return Err(EngineError::from_lance(error)),
    };
    let mut chunks = collect_chunks(stream, false).await?;
    let fetch_capped = chunks.len() >= limit;
    if let Some(glob) = glob {
        chunks.retain(|chunk| glob.is_match(&chunk.path));
    }
    Ok((chunks, fetch_capped))
}

async fn substring_candidates(
    generation: &GenerationHandle,
    query_text: &str,
    limit: usize,
    filter: &str,
    glob: Option<&GlobMatcher>,
) -> EngineResult<(Vec<ChunkWire>, bool)> {
    let scan_limit = limit.saturating_mul(4).min(2000);
    let mut query = generation
        .table
        .query()
        .select(Select::columns(CHUNK_COLUMNS))
        .limit(scan_limit);
    if !filter.is_empty() {
        query = query.only_if(filter);
    }
    let stream = query.execute().await.map_err(EngineError::from_lance)?;
    let needle = query_text.to_lowercase();
    let scanned = collect_chunks(stream, false).await?;
    let fetch_capped = scanned.len() >= scan_limit;
    let mut ranked = scanned
        .into_iter()
        .filter(|chunk| glob.is_none_or(|matcher| matcher.is_match(&chunk.path)))
        .filter_map(|chunk| {
            let score = chunk.fts_text.to_lowercase().matches(&needle).count();
            (score > 0).then_some((score, chunk))
        })
        .collect::<Vec<_>>();
    ranked.sort_by(|left, right| {
        right
            .0
            .cmp(&left.0)
            .then_with(|| left.1.path.cmp(&right.1.path))
            .then_with(|| left.1.ordinal.cmp(&right.1.ordinal))
    });
    Ok((
        ranked
            .into_iter()
            .take(limit)
            .map(|(_, chunk)| chunk)
            .collect(),
        fetch_capped,
    ))
}

async fn collect_chunks(
    stream: lancedb::arrow::SendableRecordBatchStream,
    include_vector: bool,
) -> EngineResult<Vec<ChunkWire>> {
    let batches = stream
        .try_collect::<Vec<_>>()
        .await
        .map_err(EngineError::from_lance)?;
    let mut chunks = Vec::new();
    for batch in batches {
        for row in 0..batch.num_rows() {
            chunks.push(chunk_from_batch_row(&batch, row, include_vector)?);
        }
    }
    Ok(chunks)
}

fn chunk_from_batch_row(
    batch: &RecordBatch,
    row: usize,
    include_vector: bool,
) -> EngineResult<ChunkWire> {
    Ok(ChunkWire {
        chunk_id: string_value(batch, "chunk_id", row)?,
        file_id: string_value(batch, "file_id", row)?,
        path: string_value(batch, "path", row)?,
        ext: string_value(batch, "ext", row)?,
        ordinal: int32_value(batch, "ordinal", row)?,
        heading: string_value(batch, "heading", row)?,
        start_line: int32_value(batch, "start_line", row)?,
        end_line: int32_value(batch, "end_line", row)?,
        page_start: int32_value(batch, "page_start", row)?,
        page_end: int32_value(batch, "page_end", row)?,
        slide_start: int32_value(batch, "slide_start", row)?,
        slide_end: int32_value(batch, "slide_end", row)?,
        source_type: string_value(batch, "source_type", row)?,
        locator_json: string_value(batch, "locator_json", row)?,
        content: string_value(batch, "content", row)?,
        fts_text: string_value(batch, "fts_text", row)?,
        content_hash: string_value(batch, "content_hash", row)?,
        embedding_model: string_value(batch, "embedding_model", row)?,
        embedding_dimension: int32_value(batch, "embedding_dimension", row)?,
        vector: if include_vector && batch.column_by_name("vector").is_some() {
            vector_value(batch, "vector", row)?
        } else {
            Vec::new()
        },
        updated_at: int64_value(batch, "updated_at", row)?,
    })
}

fn string_value(batch: &RecordBatch, name: &str, row: usize) -> EngineResult<String> {
    let array = batch
        .column_by_name(name)
        .ok_or_else(|| EngineError::schema(format!("missing column {name}")))?
        .as_any()
        .downcast_ref::<StringArray>()
        .ok_or_else(|| EngineError::schema(format!("column {name} is not Utf8")))?;
    if array.is_null(row) {
        Ok(String::new())
    } else {
        Ok(array.value(row).to_owned())
    }
}

fn int32_value(batch: &RecordBatch, name: &str, row: usize) -> EngineResult<i32> {
    let array = batch
        .column_by_name(name)
        .ok_or_else(|| EngineError::schema(format!("missing column {name}")))?
        .as_any()
        .downcast_ref::<Int32Array>()
        .ok_or_else(|| EngineError::schema(format!("column {name} is not Int32")))?;
    Ok(if array.is_null(row) {
        0
    } else {
        array.value(row)
    })
}

fn int64_value(batch: &RecordBatch, name: &str, row: usize) -> EngineResult<i64> {
    let array = batch
        .column_by_name(name)
        .ok_or_else(|| EngineError::schema(format!("missing column {name}")))?
        .as_any()
        .downcast_ref::<Int64Array>()
        .ok_or_else(|| EngineError::schema(format!("column {name} is not Int64")))?;
    Ok(if array.is_null(row) {
        0
    } else {
        array.value(row)
    })
}

fn vector_value(batch: &RecordBatch, name: &str, row: usize) -> EngineResult<Vec<f32>> {
    let array = batch
        .column_by_name(name)
        .ok_or_else(|| EngineError::schema(format!("missing column {name}")))?
        .as_any()
        .downcast_ref::<FixedSizeListArray>()
        .ok_or_else(|| EngineError::schema(format!("column {name} is not FixedSizeList")))?;
    if array.is_null(row) {
        return Ok(Vec::new());
    }
    let values = array.value(row);
    let values = values
        .as_any()
        .downcast_ref::<Float32Array>()
        .ok_or_else(|| EngineError::schema(format!("column {name} items are not Float32")))?;
    Ok(values
        .iter()
        .map(|value| value.unwrap_or(f32::NAN))
        .collect())
}

fn search_filter(request: &SearchRequest) -> String {
    let mut filters = Vec::new();
    if !request.path_prefix.is_empty() {
        let prefix = request.path_prefix.trim_end_matches('/');
        filters.push(format!(
            "(path = '{}' OR starts_with(path, '{}'))",
            sql_literal(prefix),
            sql_literal(&format!("{prefix}/"))
        ));
    }
    if !request.file_type.is_empty() {
        filters.push(format!(
            "ext = '{}'",
            sql_literal(&normalize_ext(&request.file_type))
        ));
    }
    filters.join(" AND ")
}

fn sql_literal(value: &str) -> String {
    value.replace('\'', "''")
}

fn normalize_ext(value: &str) -> String {
    let ext = value.trim().to_ascii_lowercase();
    if ext.is_empty() || ext.starts_with('.') {
        ext
    } else {
        format!(".{ext}")
    }
}

#[allow(clippy::too_many_arguments)]
fn search_is_truncated(
    vector_candidates: usize,
    fts_candidates: usize,
    candidate_max: usize,
    glob_filtered: bool,
    vector_fetch_capped: bool,
    fts_fetch_capped: bool,
    offset: usize,
    limit: usize,
    match_count: usize,
) -> bool {
    vector_candidates >= candidate_max
        || fts_candidates >= candidate_max
        || (glob_filtered && (vector_fetch_capped || fts_fetch_capped))
        || offset.saturating_add(limit) < match_count
}

// Match the Go runtime's existing glob contract: `*` and `?` never cross a
// slash, `**/` may match zero or more directories, and character classes or
// brace expansion are literals rather than extra syntax.
fn compile_path_glob(pattern: &str) -> EngineResult<GlobMatcher> {
    let mut escaped = String::with_capacity(pattern.len());
    for character in pattern.chars() {
        if matches!(character, '\\' | '[' | ']' | '{' | '}') {
            escaped.push('\\');
        }
        escaped.push(character);
    }
    let mut builder = GlobBuilder::new(&escaped);
    builder.literal_separator(true).backslash_escape(true);
    builder
        .build()
        .map(|glob| glob.compile_matcher())
        .map_err(|error| EngineError::query(format!("invalid pathGlob: {error}")))
}

fn validate_tokenizer(value: &str) -> EngineResult<()> {
    match value.to_ascii_lowercase().as_str() {
        "icu" | "simple" | "whitespace" | "ngram" => Ok(()),
        _ => Err(EngineError::invalid(
            "ftsTokenizer must be one of icu, simple, whitespace, ngram",
        )),
    }
}

fn fts_index_builder(tokenizer: &str) -> EngineResult<FtsIndexBuilder> {
    validate_tokenizer(tokenizer)?;
    let base = if tokenizer.eq_ignore_ascii_case("icu") {
        "whitespace"
    } else {
        tokenizer
    };
    Ok(FtsIndexBuilder::default()
        .base_tokenizer(base.to_ascii_lowercase())
        .with_position(false)
        .lower_case(true)
        .stem(false)
        .remove_stop_words(false)
        .ascii_folding(true))
}

fn tokenize_for_index(value: &str, tokenizer: &str) -> String {
    if !tokenizer.eq_ignore_ascii_case("icu") {
        return value.to_owned();
    }
    let segmenter = WordSegmenter::new_auto(Default::default());
    let boundaries = segmenter.segment_str(value).collect::<Vec<_>>();
    boundaries
        .windows(2)
        .filter_map(|window| value.get(window[0]..window[1]))
        .map(str::trim)
        .filter(|segment| !segment.is_empty())
        .filter(|segment| segment.chars().any(char::is_alphanumeric))
        .collect::<Vec<_>>()
        .join(" ")
}

fn is_fts_query_error(message: &str) -> bool {
    let lower = message.to_ascii_lowercase();
    lower.contains("parse") || lower.contains("query syntax") || lower.contains("invalid query")
}

async fn index_info(table: &Table) -> EngineResult<Vec<IndexInfo>> {
    let configs = table
        .list_indices()
        .await
        .map_err(EngineError::from_lance)?;
    let mut output = Vec::with_capacity(configs.len());
    for config in configs {
        let stats = table
            .index_stats(&config.name)
            .await
            .map_err(EngineError::from_lance)?;
        output.push(IndexInfo {
            name: config.name,
            index_type: config.index_type.to_string(),
            columns: config.columns,
            indexed_rows: stats.as_ref().map(|stats| stats.num_indexed_rows),
            unindexed_rows: stats.as_ref().map(|stats| stats.num_unindexed_rows),
        });
    }
    output.sort_by(|left, right| left.name.cmp(&right.name));
    Ok(output)
}

async fn validate_ann_recall(
    table: &Table,
    sample_count: usize,
    result_count: usize,
) -> EngineResult<f32> {
    let row_count = table
        .count_rows(None)
        .await
        .map_err(EngineError::from_lance)?;
    let actual_sample_count = sample_count.min(row_count);
    let mut samples = Vec::with_capacity(actual_sample_count);
    for index in 0..actual_sample_count {
        let offset = if actual_sample_count <= 1 {
            0
        } else {
            index * (row_count - 1) / (actual_sample_count - 1)
        };
        let batches = table
            .query()
            .select(Select::columns(&["vector"]))
            .offset(offset)
            .limit(1)
            .execute()
            .await
            .map_err(EngineError::from_lance)?
            .try_collect::<Vec<_>>()
            .await
            .map_err(EngineError::from_lance)?;
        for batch in batches {
            for row in 0..batch.num_rows() {
                samples.push(vector_value(&batch, "vector", row)?);
            }
        }
    }
    if samples.is_empty() {
        return Err(EngineError::index_not_ready(
            "cannot validate ANN recall without sample vectors",
        ));
    }

    let mut recalls = Vec::with_capacity(samples.len());
    for vector in samples {
        let indexed = vector_result_ids(table, &vector, result_count, false).await?;
        let flat = vector_result_ids(table, &vector, result_count, true).await?;
        recalls.push(recall_at_k(&indexed, &flat));
    }
    Ok(recalls.iter().sum::<f32>() / recalls.len() as f32)
}

async fn vector_result_ids(
    table: &Table,
    vector: &[f32],
    limit: usize,
    bypass_index: bool,
) -> EngineResult<Vec<String>> {
    let query = table
        .vector_search(vector)
        .map_err(EngineError::from_lance)?
        .distance_type(DistanceType::Cosine)
        .select(Select::columns(&["chunk_id"]))
        .limit(limit);
    let query = if bypass_index {
        query.bypass_vector_index()
    } else {
        query
            .nprobes(ANN_NPROBES)
            .ef(ann_search_ef(limit))
            .refine_factor(ANN_REFINE_FACTOR)
    };
    let batches = query
        .execute()
        .await
        .map_err(EngineError::from_lance)?
        .try_collect::<Vec<_>>()
        .await
        .map_err(EngineError::from_lance)?;
    let mut ids = Vec::new();
    for batch in batches {
        for row in 0..batch.num_rows() {
            ids.push(string_value(&batch, "chunk_id", row)?);
        }
    }
    Ok(ids)
}

fn recall_at_k(indexed: &[String], ground_truth: &[String]) -> f32 {
    if ground_truth.is_empty() {
        return 1.0;
    }
    let indexed = indexed.iter().collect::<HashSet<_>>();
    let matches = ground_truth
        .iter()
        .filter(|chunk_id| indexed.contains(chunk_id))
        .count();
    matches as f32 / ground_truth.len() as f32
}

fn ann_search_ef(limit: usize) -> usize {
    limit.max(limit.saturating_mul(4).clamp(64, 2_048))
}

fn stable_id_digest(ids: &HashSet<String>) -> String {
    let mut sorted = ids.iter().map(String::as_str).collect::<Vec<_>>();
    sorted.sort_unstable_by(|left, right| left.as_bytes().cmp(right.as_bytes()));
    let mut hasher = Sha256::new();
    for id in sorted {
        hasher.update((id.len() as u64).to_be_bytes());
        hasher.update(id.as_bytes());
    }
    let digest = hasher.finalize();
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut encoded = String::with_capacity(digest.len() * 2);
    for byte in digest {
        encoded.push(HEX[(byte >> 4) as usize] as char);
        encoded.push(HEX[(byte & 0x0f) as usize] as char);
    }
    encoded
}

fn chunk_validation_item(chunk: &ChunkWire) -> Vec<u8> {
    let mut item = Vec::with_capacity(256);
    for value in [
        chunk.chunk_id.as_str(),
        chunk.file_id.as_str(),
        chunk.path.as_str(),
        chunk.heading.as_str(),
        chunk.source_type.as_str(),
        chunk.locator_json.as_str(),
        chunk.content_hash.as_str(),
    ] {
        item.extend_from_slice(&(value.len() as u64).to_be_bytes());
        item.extend_from_slice(value.as_bytes());
    }
    for value in [
        chunk.ordinal,
        chunk.start_line,
        chunk.end_line,
        chunk.page_start,
        chunk.page_end,
        chunk.slide_start,
        chunk.slide_end,
    ] {
        item.extend_from_slice(&(value as i64).to_be_bytes());
    }
    item
}

fn chunk_validation_set_hash(mut items: Vec<Vec<u8>>) -> String {
    items.sort_unstable();
    let mut hasher = Sha256::new();
    for item in items {
        hasher.update((item.len() as u64).to_be_bytes());
        hasher.update(item);
    }
    format!("sha256:{:x}", hasher.finalize())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn base_request() -> BaseRequest {
        BaseRequest {
            request_id: "request-1".to_owned(),
            agent_key: "agent-1".to_owned(),
            generation_id: "generation_1".to_owned(),
        }
    }

    fn test_chunk(
        id: &str,
        file_id: &str,
        path: &str,
        content: &str,
        vector: [f32; 2],
    ) -> ChunkWire {
        ChunkWire {
            chunk_id: id.to_owned(),
            file_id: file_id.to_owned(),
            path: path.to_owned(),
            ext: "md".to_owned(),
            ordinal: 0,
            start_line: 1,
            end_line: 20,
            source_type: "text".to_owned(),
            content: content.to_owned(),
            content_hash: format!("hash-{id}"),
            embedding_model: "test".to_owned(),
            embedding_dimension: 2,
            vector: vector.to_vec(),
            updated_at: 1,
            ..ChunkWire::default()
        }
    }

    #[test]
    fn icu_tokenizes_chinese_and_english() {
        let tokenized = tokenize_for_index("本地知识库 LanceDB search", "icu");
        assert!(tokenized.contains("本地"));
        assert!(tokenized.contains("LanceDB"));
        assert!(!tokenized.contains("  "));
    }

    #[test]
    fn escapes_sql_literals() {
        assert_eq!(sql_literal("a'b"), "a''b");
    }

    #[test]
    fn path_glob_matches_legacy_anchored_semantics() {
        let root_only = compile_path_glob("*.md").unwrap();
        assert!(root_only.is_match("alpha.md"));
        assert!(!root_only.is_match("docs/alpha.md"));

        let recursive = compile_path_glob("**/*.md").unwrap();
        assert!(recursive.is_match("alpha.md"));
        assert!(recursive.is_match("docs/alpha.md"));

        let literal_class = compile_path_glob("[ab].md").unwrap();
        assert!(literal_class.is_match("[ab].md"));
        assert!(!literal_class.is_match("a.md"));
    }

    #[test]
    fn glob_overfetch_cap_marks_search_truncated_before_post_filter() {
        assert!(search_is_truncated(3, 2, 500, true, true, false, 0, 8, 5));
        assert!(!search_is_truncated(3, 2, 500, true, false, false, 0, 8, 5));
    }

    #[test]
    fn chunk_batch_round_trip() {
        let chunk = ChunkWire {
            chunk_id: "c1".to_owned(),
            file_id: "f1".to_owned(),
            path: "docs/a.md".to_owned(),
            ext: ".md".to_owned(),
            ordinal: 1,
            heading: "Heading".to_owned(),
            content: "Content".to_owned(),
            fts_text: "Heading Content".to_owned(),
            embedding_dimension: 2,
            vector: vec![0.5, 0.25],
            ..ChunkWire::default()
        };
        let mut reader = chunk_reader(&[chunk], 2).unwrap();
        let batch = reader.next().unwrap().unwrap();
        let decoded = chunk_from_batch_row(&batch, 0, true).unwrap();
        assert_eq!(decoded.chunk_id, "c1");
        assert_eq!(decoded.vector, vec![0.5, 0.25]);
    }

    #[test]
    fn arrow_ipc_round_trip() {
        let chunk = test_chunk("c1", "f1", "docs/a.md", "arrow payload", [0.5, 0.25]);
        let mut reader = chunk_reader(&[chunk], 2).unwrap();
        let batch = reader.next().unwrap().unwrap();
        let mut payload = Vec::new();
        {
            let mut writer =
                arrow_ipc::writer::StreamWriter::try_new(&mut payload, &batch.schema()).unwrap();
            writer.write(&batch).unwrap();
            writer.finish().unwrap();
        }
        let decoded = chunks_from_arrow_ipc(&payload).unwrap();
        assert_eq!(decoded.len(), 1);
        assert_eq!(decoded[0].chunk_id, "c1");
        assert_eq!(decoded[0].vector, vec![0.5, 0.25]);
    }

    #[test]
    fn read_path_uses_one_based_source_lines() {
        let chunks = vec![
            ChunkWire {
                chunk_id: "c1".to_owned(),
                ordinal: 0,
                start_line: 1,
                end_line: 10,
                ..ChunkWire::default()
            },
            ChunkWire {
                chunk_id: "c2".to_owned(),
                ordinal: 1,
                start_line: 11,
                end_line: 20,
                ..ChunkWire::default()
            },
            ChunkWire {
                chunk_id: "c3".to_owned(),
                ordinal: 2,
                start_line: 21,
                end_line: 30,
                ..ChunkWire::default()
            },
        ];
        let selected = select_path_chunks(chunks, 12, 10);
        assert_eq!(
            selected
                .iter()
                .map(|chunk| chunk.chunk_id.as_str())
                .collect::<Vec<_>>(),
            vec!["c2", "c3"]
        );
    }

    #[test]
    fn recall_uses_flat_results_as_ground_truth() {
        let indexed = vec!["a".to_owned(), "b".to_owned(), "x".to_owned()];
        let flat = vec!["a".to_owned(), "b".to_owned(), "c".to_owned()];
        assert!((recall_at_k(&indexed, &flat) - (2.0 / 3.0)).abs() < f32::EPSILON);
        assert_eq!(recall_at_k(&[], &[]), 1.0);
    }

    #[test]
    fn ann_search_ef_expands_small_candidate_sets_without_falling_below_limit() {
        assert_eq!(ann_search_ef(1), 64);
        assert_eq!(ann_search_ef(10), 64);
        assert_eq!(ann_search_ef(30), 120);
        assert_eq!(ann_search_ef(500), 2_000);
        assert_eq!(ann_search_ef(2_000), 2_048);
        assert_eq!(ann_search_ef(3_000), 3_000);
    }

    #[test]
    fn stable_id_digest_is_order_independent_and_unambiguous() {
        let left = HashSet::from(["bc".to_owned(), "a".to_owned()]);
        let reordered = HashSet::from(["a".to_owned(), "bc".to_owned()]);
        let different_boundaries = HashSet::from(["ab".to_owned(), "c".to_owned()]);
        assert_eq!(stable_id_digest(&left), stable_id_digest(&reordered));
        assert_ne!(
            stable_id_digest(&left),
            stable_id_digest(&different_boundaries)
        );
        assert_eq!(
            stable_id_digest(&HashSet::new()),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
    }

    #[tokio::test]
    async fn engine_generation_round_trip_is_idempotent() {
        let temp = tempfile::tempdir().unwrap();
        let engine = Engine::new(vec![temp.path().to_path_buf()]).unwrap();
        let create = || CreateGenerationRequest {
            base: base_request(),
            storage_dir: temp.path().to_string_lossy().into_owned(),
            vector_dimension: 2,
            embedding_model: "test".to_owned(),
            fts_tokenizer: "icu".to_owned(),
        };
        let first = engine.create_generation(create()).await.unwrap();
        assert!(first.created);
        let second = engine.create_generation(create()).await.unwrap();
        assert!(!second.created);

        let import_chunks = vec![
            test_chunk("c1", "f1", "docs/local.md", "本地知识库", [1.0, 0.0]),
            test_chunk("c2", "f2", "docs/remote.md", "remote search", [0.0, 1.0]),
        ];
        for _ in 0..2 {
            engine
                .import_chunks(ImportRequest {
                    base: base_request(),
                    chunks: import_chunks.clone(),
                })
                .await
                .unwrap();
        }
        let imported_stats = engine
            .stats(StatsRequest {
                base: base_request(),
            })
            .await
            .unwrap();
        assert_eq!(imported_stats.row_count, 2);
        assert_eq!(imported_stats.file_count, 2);
        let indexed = engine
            .build_indexes(BuildIndexesRequest {
                base: base_request(),
                ann_min_rows: 1000,
                fts_tokenizer: "icu".to_owned(),
            })
            .await
            .unwrap();
        assert_eq!(indexed.vector_index_type, "flat");

        let search: SearchRequest = serde_json::from_value(serde_json::json!({
            "requestId": "search-1",
            "agentKey": "agent-1",
            "generationId": "generation_1",
            "query": "本地知识库",
            "vector": [1.0, 0.0],
            "limit": 2,
            "pathPrefix": "docs",
            "pathGlob": "docs/*.md",
            "type": "md"
        }))
        .unwrap();
        let result = engine.search(search).await.unwrap();
        assert_eq!(result.matches.first().unwrap().chunk.chunk_id, "c1");

        let validation = engine
            .validate(ValidateRequest {
                base: base_request(),
                expected_chunk_count: Some(2),
                expected_file_count: Some(2),
            })
            .await
            .unwrap();
        assert!(validation.valid);

        let replaced = engine
            .replace_file(ReplaceFileRequest {
                base: base_request(),
                file_id: "f1".to_owned(),
                chunks: vec![test_chunk(
                    "c3",
                    "f1",
                    "docs/local.md",
                    "updated local knowledge",
                    [1.0, 0.0],
                )],
            })
            .await
            .unwrap();
        assert_eq!(replaced.inserted_rows, 1);
        assert_eq!(replaced.deleted_rows, 1);
        assert!(
            engine
                .read_chunk(ReadChunkRequest {
                    base: base_request(),
                    chunk_id: "c1".to_owned(),
                })
                .await
                .unwrap()
                .chunk
                .is_none()
        );

        let read = engine
            .read_path(ReadPathRequest {
                base: base_request(),
                path: "docs/local.md".to_owned(),
                offset: 1,
                limit: 200,
            })
            .await
            .unwrap();
        assert_eq!(read.chunks.len(), 1);
        assert_eq!(read.chunks[0].chunk_id, "c3");

        let deleted = engine
            .delete_file(DeleteFileRequest {
                base: base_request(),
                file_id: "f2".to_owned(),
            })
            .await
            .unwrap();
        assert_eq!(deleted.deleted_rows, 1);

        let release = || ReleaseGenerationRequest {
            base: base_request(),
        };
        assert!(engine.release_generation(release()).await.unwrap().released);
        assert!(!engine.release_generation(release()).await.unwrap().released);
        assert_eq!(engine.registered_generation_count().await, 0);
    }

    #[tokio::test]
    async fn empty_generation_builds_and_validates() {
        let temp = tempfile::tempdir().unwrap();
        let engine = Engine::new(vec![temp.path().to_path_buf()]).unwrap();
        engine
            .create_generation(CreateGenerationRequest {
                base: base_request(),
                storage_dir: temp.path().to_string_lossy().into_owned(),
                vector_dimension: 2,
                embedding_model: "test".to_owned(),
                fts_tokenizer: "icu".to_owned(),
            })
            .await
            .unwrap();
        let indexed = engine
            .build_indexes(BuildIndexesRequest {
                base: base_request(),
                ann_min_rows: 1000,
                fts_tokenizer: "icu".to_owned(),
            })
            .await
            .unwrap();
        assert_eq!(indexed.row_count, 0);
        assert_eq!(indexed.vector_index_type, "flat");
        let validation = engine
            .validate(ValidateRequest {
                base: base_request(),
                expected_chunk_count: Some(0),
                expected_file_count: Some(0),
            })
            .await
            .unwrap();
        assert!(validation.valid);
        assert_eq!(validation.row_count, 0);
        assert_eq!(validation.file_count, 0);
    }
}
