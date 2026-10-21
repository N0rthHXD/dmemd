package embedding

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ErrInvalidVector identifies a response that cannot be indexed. It is a
// permanent failure: retrying the same model input cannot make the shape valid.
var ErrInvalidVector = errors.New("embedding: 无效向量")

// ValidateVectors checks the provider response contract before any value can be
// stored: one vector per input, requested dimensions, finite components and a
// non-zero norm. The returned slices are not modified.
func ValidateVectors(vectors [][]float32, count, dimensions int) error {
	if dimensions <= 0 {
		return fmt.Errorf("%w: 维数必须大于 0", ErrInvalidVector)
	}
	if len(vectors) != count {
		return fmt.Errorf("%w: 返回 %d 条，期望 %d 条", ErrInvalidVector, len(vectors), count)
	}
	for index, vector := range vectors {
		if len(vector) != dimensions {
			return fmt.Errorf("%w: 第 %d 条维数为 %d，期望 %d", ErrInvalidVector, index, len(vector), dimensions)
		}
		var squared float64
		for _, value := range vector {
			converted := float64(value)
			if math.IsNaN(converted) || math.IsInf(converted, 0) {
				return fmt.Errorf("%w: 第 %d 条包含非有限值", ErrInvalidVector, index)
			}
			squared += converted * converted
		}
		if squared == 0 || math.IsInf(squared, 0) || math.IsNaN(squared) {
			return fmt.Errorf("%w: 第 %d 条范数为零或不可用", ErrInvalidVector, index)
		}
	}
	return nil
}

// NormalizeL2 normalizes one validated vector in place and returns it.
func NormalizeL2(vector []float32) ([]float32, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("%w: 空向量", ErrInvalidVector)
	}
	var squared float64
	for _, value := range vector {
		converted := float64(value)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return nil, fmt.Errorf("%w: 包含非有限值", ErrInvalidVector)
		}
		squared += converted * converted
	}
	if squared == 0 || math.IsInf(squared, 0) || math.IsNaN(squared) {
		return nil, fmt.Errorf("%w: 范数为零或不可用", ErrInvalidVector)
	}
	norm := math.Sqrt(squared)
	for index, value := range vector {
		vector[index] = float32(float64(value) / norm)
	}
	return vector, nil
}

// EncodeVector validates, L2-normalizes and serializes one vector as
// little-endian float32 values.
func EncodeVector(vector []float32, dimensions int) ([]byte, error) {
	if len(vector) != dimensions {
		return nil, fmt.Errorf("%w: 维数为 %d，期望 %d", ErrInvalidVector, len(vector), dimensions)
	}
	normalized, err := NormalizeL2(vector)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, len(normalized)*4)
	for index, value := range normalized {
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(value))
	}
	return encoded, nil
}

// DecodeVector reverses EncodeVector. A BLOB whose length does not match the
// declared dimensions is rejected rather than partially interpreted.
func DecodeVector(encoded []byte, dimensions int) ([]float32, error) {
	if dimensions <= 0 || len(encoded) != dimensions*4 {
		return nil, fmt.Errorf("%w: BLOB 长度 %d 与维数 %d 不匹配", ErrInvalidVector, len(encoded), dimensions)
	}
	vector := make([]float32, dimensions)
	var squared float64
	for index := range vector {
		value := math.Float32frombits(binary.LittleEndian.Uint32(encoded[index*4:]))
		converted := float64(value)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return nil, fmt.Errorf("%w: 包含非有限值", ErrInvalidVector)
		}
		squared += converted * converted
		vector[index] = value
	}
	if squared == 0 || math.IsNaN(squared) || math.IsInf(squared, 0) {
		return nil, fmt.Errorf("%w: 范数为零或不可用", ErrInvalidVector)
	}
	return vector, nil
}

// DotProduct is the cosine similarity for two L2-normalized vectors.
func DotProduct(left, right []float32) (float64, error) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, fmt.Errorf("%w: 点积维数不一致", ErrInvalidVector)
	}
	var sum float64
	for index, value := range left {
		sum += float64(value) * float64(right[index])
	}
	return sum, nil
}
