package app

func copyFloat64Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func copyContextUsage(raw *SessionContextUsageSnapshot) *SessionContextUsageSnapshot {
	if raw == nil {
		return nil
	}
	return &SessionContextUsageSnapshot{
		UsedTokens:  copyIntPtr(raw.UsedTokens),
		TotalTokens: copyIntPtr(raw.TotalTokens),
		PercentUsed: copyIntPtr(raw.PercentUsed),
	}
}

func copyTurnTiming(raw *SessionTurnTimingSnapshot) *SessionTurnTimingSnapshot {
	if raw == nil {
		return nil
	}
	return &SessionTurnTimingSnapshot{
		StartedTS:   raw.StartedTS,
		LastEventTS: copyFloat64Ptr(raw.LastEventTS),
	}
}

func mergeTurnTiming(current, next *SessionTurnTimingSnapshot) *SessionTurnTimingSnapshot {
	if current == nil {
		return copyTurnTiming(next)
	}
	if next == nil {
		return copyTurnTiming(current)
	}
	merged := copyTurnTiming(current)
	if next.StartedTS > 0 {
		merged.StartedTS = next.StartedTS
		if merged.LastEventTS != nil && *merged.LastEventTS < next.StartedTS {
			merged.LastEventTS = nil
		}
	}
	if next.LastEventTS != nil {
		merged.LastEventTS = copyFloat64Ptr(next.LastEventTS)
	}
	return merged
}
