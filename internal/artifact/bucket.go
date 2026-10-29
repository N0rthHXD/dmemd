package artifact

import (
	"strconv"
	"strings"
	"time"
)

// DefaultL1BucketSize is the code-generated local-aggregation window used when
// no explicit bucket size is configured. A bucket is never chosen by a model.
const DefaultL1BucketSize = 24 * time.Hour

// L1BucketForTime returns the canonical, deterministic bucket label containing
// at. The label carries both the window start and its size so input selection
// can recover the exact half-open range without consulting mutable state.
func L1BucketForTime(at time.Time, size time.Duration) string {
	if size <= 0 {
		size = DefaultL1BucketSize
	}
	seconds := int64(size / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	at = at.UTC()
	startUnix := at.Unix() - at.Unix()%seconds
	start := time.Unix(startUnix, 0).UTC()
	return start.Format(time.RFC3339) + "/" + strconv.FormatInt(seconds, 10)
}

// L1BucketRange decodes one L1 bucket label. An unparseable label is a legacy
// or externally supplied target and yields false; callers then keep their
// existing scope-wide selection instead of guessing a time window.
func L1BucketRange(bucket string) (time.Time, time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(bucket), "/")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, false
	}
	start, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	seconds, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, time.Time{}, false
	}
	return start.UTC(), start.UTC().Add(time.Duration(seconds) * time.Second), true
}
