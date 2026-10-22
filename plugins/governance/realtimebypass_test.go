package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * Bifrost 16.3 (#324), governance half: the bypass-attempt registry — the auditable record
 * the 全入口 enumeration (22.x) cites. The refusal behaviour itself is the transports'
 * admission (realtimeauthgate.go, pinned there); what this file pins is that every recorded
 * attempt is countable by its closed kind, unrecordable under an unnamed kind, bounded in
 * memory under a flood, and readable as a copy no consumer can mutate.
 */

func TestRealtimeBypassRegistryCountsByClosedKind(t *testing.T) {
	ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(ClearRealtimeBypassAttemptsForTest)

	RecordRealtimeBypassAttempt(RealtimeBypassUnmappedEphemeralToken, "webrtc establishment presented an unmapped ek_")
	RecordRealtimeBypassAttempt(RealtimeBypassEphemeralTokenReuse, "websocket establishment re-spent an ek_bf_")
	RecordRealtimeBypassAttempt(RealtimeBypassUnmappedEphemeralToken, "websocket-transcription establishment presented an unmapped ek_")

	stats := RealtimeBypassStatistics()
	assert.EqualValues(t, 3, stats.Total)
	assert.EqualValues(t, 2, stats.ByKind[RealtimeBypassUnmappedEphemeralToken])
	assert.EqualValues(t, 1, stats.ByKind[RealtimeBypassEphemeralTokenReuse])

	attempts := SnapshotRealtimeBypassAttempts()
	require.Len(t, attempts, 3)
	assert.Equal(t, RealtimeBypassUnmappedEphemeralToken, attempts[0].Kind)
	assert.Equal(t, RealtimeBypassEphemeralTokenReuse, attempts[1].Kind)
	assert.False(t, attempts[0].At.After(attempts[2].At), "the records answer in recorded order")
}

func TestRealtimeBypassRegistryRefusesUnnamedKinds(t *testing.T) {
	ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(ClearRealtimeBypassAttemptsForTest)

	RecordRealtimeBypassAttempt("some_new_kind", "a future writer's kind")
	assert.EqualValues(t, 0, RealtimeBypassStatistics().Total,
		"an unnamed kind books nothing — a contract change is the only way it becomes countable")
}

func TestRealtimeBypassRegistryStaysBoundedUnderAFlood(t *testing.T) {
	ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(ClearRealtimeBypassAttemptsForTest)

	for i := 0; i < realtimeBypassLogLimit+40; i++ {
		RecordRealtimeBypassAttempt(RealtimeBypassUnmappedEphemeralToken, "flood")
	}
	stats := RealtimeBypassStatistics()
	assert.EqualValues(t, realtimeBypassLogLimit+40, stats.Total,
		"the counters stay exact for any volume")
	assert.EqualValues(t, realtimeBypassLogLimit, stats.Attempts,
		"the retained records stay bounded — the newest evidence, never unbounded memory")
}

func TestRealtimeBypassSnapshotIsACopy(t *testing.T) {
	ClearRealtimeBypassAttemptsForTest()
	t.Cleanup(ClearRealtimeBypassAttemptsForTest)

	RecordRealtimeBypassAttempt(RealtimeBypassEphemeralTokenReuse, "original")
	attempts := SnapshotRealtimeBypassAttempts()
	attempts[0].Detail = "mutated by a reader"

	fresh := SnapshotRealtimeBypassAttempts()
	assert.Equal(t, "original", fresh[0].Detail, "no reader can rewrite the record another reads")
}
