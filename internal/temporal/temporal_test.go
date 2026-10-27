package temporal

import (
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestParseValueKeepsResolution(t *testing.T) {
	date, ok := ParseValue("2026-10-08")
	if !ok || date.Time == nil || date.Precision != memory.PrecisionDay || date.TimezoneKnown {
		t.Fatalf("date-only = %+v ok=%v", date, ok)
	}
	if date.Time.UTC().Hour() != 0 {
		t.Fatalf("date-only normalized at %v", date.Time)
	}
	instant, ok := ParseValue("2026-10-08T09:30:00+08:00")
	if !ok || instant.Precision != memory.PrecisionSecond || !instant.TimezoneKnown {
		t.Fatalf("instant = %+v ok=%v", instant, ok)
	}
	if _, ok := ParseValue("2026-10"); ok {
		t.Fatal("month-only value became an instant")
	}
}

func TestFindMentionRejectsWrongYearAndMonthOnly(t *testing.T) {
	value := time.Date(2027, 10, 8, 0, 0, 0, 0, time.UTC)
	if _, ok := FindMention("2026年10月8日拍摄", value, memory.PrecisionUnknown); ok {
		t.Fatal("wrong year matched")
	}
	matched, ok := FindMention("2026年10月8日拍摄", time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), memory.PrecisionUnknown)
	if !ok || matched.Precision != memory.PrecisionDay || matched.TimezoneKnown {
		t.Fatalf("day mention = %+v ok=%v", matched, ok)
	}
	if _, ok := FindMention("10月拍摄", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), memory.PrecisionUnknown); ok {
		t.Fatal("month-only wording matched a day")
	}
}

func TestRelativeAndQuantityStripping(t *testing.T) {
	if !HasRelativeTime("明天下午见面") || HasRelativeTime("2026年10月8日见面") {
		t.Fatal("relative time detection is wrong")
	}
	if HasMonthOnly("2026年10月8日") && len(Mentions("2026年10月8日")) == 0 {
		t.Fatal("full date reported as month-only without a complete mention")
	}
	if got := StripInstants("2026年10月8日服用 5 mg"); got == "" || containsDigits(got, "2026") {
		t.Fatalf("StripInstants = %q", got)
	}
}

func containsDigits(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
