package artifact

import (
	"testing"
	"time"
)

func TestL1BucketRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 29, 8, 35, 12, 0, time.UTC)
	bucket := L1BucketForTime(at, 6*time.Hour)
	start, end, ok := L1BucketRange(bucket)
	if !ok {
		t.Fatalf("bucket %q did not parse", bucket)
	}
	if !start.Equal(time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("bucket start = %s", start)
	}
	if end.Sub(start) != 6*time.Hour || at.Before(start) || !at.Before(end) {
		t.Fatalf("bucket range = %s..%s", start, end)
	}
	if same := L1BucketForTime(at.Add(10*time.Minute), 6*time.Hour); same != bucket {
		t.Fatalf("same window produced %q and %q", bucket, same)
	}
	if _, _, ok := L1BucketRange("legacy-bucket"); ok {
		t.Fatal("legacy label unexpectedly parsed as a time range")
	}
}
