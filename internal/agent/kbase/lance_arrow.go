package kbase

import (
	"bytes"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func encodeLanceChunksIPC(chunks []lanceChunkWire) ([]byte, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	dimension := chunks[0].EmbeddingDimension
	if dimension <= 0 || len(chunks[0].Vector) != dimension {
		return nil, fmt.Errorf("invalid Arrow vector dimension %d", dimension)
	}
	fields := []arrow.Field{
		{Name: "chunk_id", Type: arrow.BinaryTypes.String},
		{Name: "file_id", Type: arrow.BinaryTypes.String},
		{Name: "path", Type: arrow.BinaryTypes.String},
		{Name: "ext", Type: arrow.BinaryTypes.String},
		{Name: "ordinal", Type: arrow.PrimitiveTypes.Int32},
		{Name: "heading", Type: arrow.BinaryTypes.String},
		{Name: "start_line", Type: arrow.PrimitiveTypes.Int32},
		{Name: "end_line", Type: arrow.PrimitiveTypes.Int32},
		{Name: "page_start", Type: arrow.PrimitiveTypes.Int32},
		{Name: "page_end", Type: arrow.PrimitiveTypes.Int32},
		{Name: "slide_start", Type: arrow.PrimitiveTypes.Int32},
		{Name: "slide_end", Type: arrow.PrimitiveTypes.Int32},
		{Name: "source_type", Type: arrow.BinaryTypes.String},
		{Name: "locator_json", Type: arrow.BinaryTypes.String},
		{Name: "content", Type: arrow.BinaryTypes.String},
		{Name: "fts_text", Type: arrow.BinaryTypes.String},
		{Name: "content_hash", Type: arrow.BinaryTypes.String},
		{Name: "embedding_model", Type: arrow.BinaryTypes.String},
		{Name: "embedding_dimension", Type: arrow.PrimitiveTypes.Int32},
		{Name: "vector", Type: arrow.FixedSizeListOf(int32(dimension), arrow.PrimitiveTypes.Float32)},
		{Name: "updated_at", Type: arrow.PrimitiveTypes.Int64},
	}
	for index := range fields {
		fields[index].Nullable = false
	}
	schema := arrow.NewSchema(fields, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	stringsAt := func(index int) *array.StringBuilder { return builder.Field(index).(*array.StringBuilder) }
	int32At := func(index int) *array.Int32Builder { return builder.Field(index).(*array.Int32Builder) }
	vectorBuilder := builder.Field(19).(*array.FixedSizeListBuilder)
	floatBuilder := vectorBuilder.ValueBuilder().(*array.Float32Builder)
	for _, chunk := range chunks {
		if chunk.EmbeddingDimension != dimension || len(chunk.Vector) != dimension {
			return nil, fmt.Errorf("chunk %s has inconsistent Arrow vector dimension", chunk.ChunkID)
		}
		for name, value := range map[string]int{
			"ordinal": chunk.Ordinal, "startLine": chunk.StartLine, "endLine": chunk.EndLine,
			"pageStart": chunk.PageStart, "pageEnd": chunk.PageEnd,
			"slideStart": chunk.SlideStart, "slideEnd": chunk.SlideEnd,
		} {
			if int64(value) < -(1<<31) || int64(value) > 1<<31-1 {
				return nil, fmt.Errorf("chunk %s %s exceeds Int32", chunk.ChunkID, name)
			}
		}
		stringsAt(0).Append(chunk.ChunkID)
		stringsAt(1).Append(chunk.FileID)
		stringsAt(2).Append(chunk.Path)
		stringsAt(3).Append(chunk.Ext)
		int32At(4).Append(int32(chunk.Ordinal))
		stringsAt(5).Append(chunk.Heading)
		int32At(6).Append(int32(chunk.StartLine))
		int32At(7).Append(int32(chunk.EndLine))
		int32At(8).Append(int32(chunk.PageStart))
		int32At(9).Append(int32(chunk.PageEnd))
		int32At(10).Append(int32(chunk.SlideStart))
		int32At(11).Append(int32(chunk.SlideEnd))
		stringsAt(12).Append(chunk.SourceType)
		stringsAt(13).Append(chunk.LocatorJSON)
		stringsAt(14).Append(chunk.Content)
		stringsAt(15).Append(chunk.FTSText)
		stringsAt(16).Append(chunk.ContentHash)
		stringsAt(17).Append(chunk.EmbeddingModel)
		int32At(18).Append(int32(chunk.EmbeddingDimension))
		vectorBuilder.Append(true)
		floatBuilder.AppendValues(chunk.Vector, nil)
		builder.Field(20).(*array.Int64Builder).Append(chunk.UpdatedAt)
	}
	record := builder.NewRecord()
	defer record.Release()
	var output bytes.Buffer
	writer := ipc.NewWriter(&output, ipc.WithSchema(schema))
	if err := writer.Write(record); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("encode KBASE Arrow record: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close KBASE Arrow stream: %w", err)
	}
	return output.Bytes(), nil
}
