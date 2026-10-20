package sqlite

import (
	"database/sql"
	"fmt"
	"time"
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
