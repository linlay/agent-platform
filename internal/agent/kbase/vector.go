package kbase

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

func encodeVector(vector []float64) ([]byte, error) {
	if len(vector) == 0 {
		return nil, nil
	}
	buf := bytes.NewBuffer(make([]byte, 0, len(vector)*8))
	for _, value := range vector {
		if err := binary.Write(buf, binary.LittleEndian, math.Float64bits(value)); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func decodeVector(data []byte) ([]float64, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data)%8 != 0 {
		return nil, fmt.Errorf("invalid vector blob length %d", len(data))
	}
	out := make([]float64, len(data)/8)
	for i := range out {
		bits := binary.LittleEndian.Uint64(data[i*8 : (i+1)*8])
		out[i] = math.Float64frombits(bits)
	}
	return out, nil
}
