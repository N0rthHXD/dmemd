package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// storageTimeLayout is fixed-width UTC RFC 3339 with nanoseconds. Fixed width is
// important: updated_at is used for lexical ordering and must agree with
// chronological ordering.
const storageTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(value time.Time) string {
	return value.UTC().Format(storageTimeLayout)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析持久化时间 %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func nullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func timeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func encodeTopics(values []string) (string, error) {
	if values == nil {
		return "[]", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("序列化主题标签: %w", err)
	}
	return string(data), nil
}

func decodeTopics(value string) ([]string, error) {
	if value == "" {
		return []string{}, nil
	}
	var topics []string
	if err := json.Unmarshal([]byte(value), &topics); err != nil {
		return nil, fmt.Errorf("解析主题标签: %w", err)
	}
	if topics == nil {
		topics = []string{}
	}
	return topics, nil
}

func encodeTemporalDetails(value memory.TemporalDetails) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("序列化时间元数据: %w", err)
	}
	return string(data), nil
}

func decodeTemporalDetails(value string) (memory.TemporalDetails, error) {
	result := memory.TemporalDetails{}
	if value == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return memory.TemporalDetails{}, fmt.Errorf("解析时间元数据: %w", err)
	}
	return result, nil
}

func encodeOptionalProcedure(value *memory.Procedure) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("序列化流程结构: %w", err)
	}
	return string(data), nil
}

func decodeOptionalProcedure(value string) (*memory.Procedure, error) {
	if value == "" {
		return nil, nil
	}
	result := &memory.Procedure{}
	if err := json.Unmarshal([]byte(value), result); err != nil {
		return nil, fmt.Errorf("解析流程结构: %w", err)
	}
	return result, nil
}

func encodeOptionalQualifiers(value *memory.AssertionQualifiers) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("序列化断言限定: %w", err)
	}
	return string(data), nil
}

func decodeOptionalQualifiers(value string) (*memory.AssertionQualifiers, error) {
	if value == "" {
		return nil, nil
	}
	result := &memory.AssertionQualifiers{}
	if err := json.Unmarshal([]byte(value), result); err != nil {
		return nil, fmt.Errorf("解析断言限定: %w", err)
	}
	return result, nil
}
