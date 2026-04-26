package app

import (
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

const sessionListPriorityHalfLife = 8 * time.Hour

var sessionListPriorityLambda = math.Ln2 / sessionListPriorityHalfLife.Seconds()

type displaySessionRecord struct {
	record         sessionRecord
	updatedAt      time.Time
	startAt        time.Time
	finalPriority  float64
	piBackend      bool
	sessionIDToken string
}

func sortSessionsForDisplay(records []sessionRecord, now time.Time) []displaySessionRecord {
	now = now.UTC()
	items := make([]displaySessionRecord, 0, len(records))
	for _, record := range records {
		items = append(items, newDisplaySessionRecord(record, now))
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		if left.finalPriority != right.finalPriority {
			return left.finalPriority > right.finalPriority
		}
		if !left.updatedAt.Equal(right.updatedAt) {
			return left.updatedAt.After(right.updatedAt)
		}
		if !left.startAt.Equal(right.startAt) {
			return left.startAt.After(right.startAt)
		}
		if left.piBackend != right.piBackend {
			return left.piBackend
		}
		return left.sessionIDToken < right.sessionIDToken
	})
	return items
}

func newDisplaySessionRecord(record sessionRecord, now time.Time) displaySessionRecord {
	updatedAt := sessionDisplayUpdatedAt(record)
	elapsedSeconds := sessionPriorityElapsedSeconds(now, updatedAt)
	timePriority := sessionPriorityFromElapsedSeconds(elapsedSeconds)
	finalPriority := clip01(timePriority + record.priorityOffset)
	if sessionSnoozed(record, now) || sessionBlocked(record) {
		finalPriority = 0
	}
	return displaySessionRecord{
		record:         record,
		updatedAt:      updatedAt,
		startAt:        record.createdAt.UTC(),
		finalPriority:  finalPriority,
		piBackend:      strings.EqualFold(record.identity.Backend().String(), "pi"),
		sessionIDToken: record.identity.SessionID().String(),
	}
}

func sessionDisplayUpdatedAt(record sessionRecord) time.Time {
	if ts := importedPISourceActivityAt(record); !ts.IsZero() {
		return ts
	}
	return record.updatedAt.UTC()
}

func importedPISourceActivityAt(record sessionRecord) time.Time {
	if !strings.EqualFold(record.identity.Backend().String(), "pi") {
		return time.Time{}
	}
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if sourcePath == "" {
		return time.Time{}
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return time.Time{}
	}
	ts := info.ModTime().UTC()
	if ts.IsZero() {
		return time.Time{}
	}
	return ts
}

func sessionPriorityElapsedSeconds(now, updatedAt time.Time) float64 {
	if updatedAt.IsZero() {
		return math.Inf(1)
	}
	elapsed := now.Sub(updatedAt).Seconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func sessionPriorityFromElapsedSeconds(elapsedSeconds float64) float64 {
	if elapsedSeconds <= 0 {
		return 1
	}
	if math.IsNaN(elapsedSeconds) || math.IsInf(elapsedSeconds, 1) {
		return 0
	}
	return clip01(math.Exp(-sessionListPriorityLambda * elapsedSeconds))
}

func clip01(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
}

func sessionSnoozed(record sessionRecord, now time.Time) bool {
	return record.snoozeUntil != nil && record.snoozeUntil.UTC().After(now)
}

func sessionBlocked(record sessionRecord) bool {
	return record.dependencySessionID != nil
}
