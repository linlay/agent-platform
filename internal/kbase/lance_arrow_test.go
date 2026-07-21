package kbase

import (
	"bytes"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
)

func TestEncodeLanceChunksIPCUsesFixedFloat32Schema(t *testing.T) {
	payload, err := encodeLanceChunksIPC([]lanceChunkWire{{
		ChunkID: "c1", FileID: "f1", Path: "docs/a.md", Ext: ".md", Ordinal: 2,
		Content: "你好 Arrow", FTSText: "docs a 你好 Arrow", ContentHash: "hash",
		EmbeddingModel: "model", EmbeddingDimension: 3, Vector: []float32{1, 2, 3}, UpdatedAt: 42,
	}})
	if err != nil {
		t.Fatalf("encode Arrow: %v", err)
	}
	reader, err := ipc.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("open Arrow stream: %v", err)
	}
	defer reader.Release()
	if !reader.Next() {
		t.Fatalf("Arrow stream has no record: %v", reader.Err())
	}
	record := reader.Record()
	if got, want := record.NumCols(), int64(21); got != want {
		t.Fatalf("columns = %d, want %d", got, want)
	}
	vector, ok := record.Column(19).(*array.FixedSizeList)
	if !ok {
		t.Fatalf("vector type = %T, want FixedSizeList", record.Column(19))
	}
	values := vector.ListValues().(*array.Float32)
	if vector.Len() != 1 || values.Len() != 3 || values.Value(2) != 3 {
		t.Fatalf("unexpected vector payload: list=%d values=%v", vector.Len(), values.Float32Values())
	}
	if got := record.Column(0).(*array.String).Value(0); got != "c1" {
		t.Fatalf("chunk_id = %q", got)
	}
}

func TestEncodeLanceChunksIPCRejectsMixedDimensions(t *testing.T) {
	_, err := encodeLanceChunksIPC([]lanceChunkWire{
		{ChunkID: "c1", EmbeddingDimension: 2, Vector: []float32{1, 2}},
		{ChunkID: "c2", EmbeddingDimension: 3, Vector: []float32{1, 2, 3}},
	})
	if err == nil {
		t.Fatal("expected mixed dimension error")
	}
}
