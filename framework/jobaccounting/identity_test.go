package jobaccounting

/*
 * Bifrost 17.1's provider-side identity pin (OpenBot #325). The OpenBot side anchors the
 * local↔provider job mapping on its own async_jobs row (local aj- identity primary,
 * provider_job_id a nullable fact behind a unique index); this file pins the two
 * properties that make that a mapping and not a drift: the provider-side record's id is
 * DERIVED, never random, and a job that already exists is never re-identified by a later
 * drive. Whichever side learns the pair second can always recompute the other's key.
 */

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The derived id is a pure function of (kind, provider, providerJobID): the same triple
// always names the same row, and no two different triples collide — including the batch
// kind's documented "" alias.
func TestProviderJobIdentityDerivationIsStable(t *testing.T) {
	assert.Equal(t,
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_123"),
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_123"),
		"the same triple derives the same id, so a recompute can never mint a second row")

	assert.Equal(t,
		cstables.ProviderJobID("", "openai", "batch_123"),
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_123"),
		"the empty kind is the batch kind, as the derivation documents")

	assert.NotEqual(t,
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_123"),
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "anthropic", "batch_123"),
		"the same provider job id under two providers is two jobs, never one")

	assert.NotEqual(t,
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_123"),
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_124"),
		"one provider's ids do not collide with each other")

	assert.NotEqual(t,
		cstables.ProviderJobID(cstables.ProviderJobKindBatch, "openai", "batch_123"),
		cstables.ProviderJobID(cstables.ProviderJobKindVideo, "openai", "batch_123"),
		"two job families sharing a provider job id stay two rows")
}

// A job that already exists keeps its identity across drives: the first accounting pass
// mints the derived id, a re-drive that arrives WITH the existing job (a sweeper's list,
// a second /results fetch) neither mints a second row nor re-attributes the first, and a
// re-drive that arrives WITHOUT one recomputes the same derived id and converges on the
// same row. There is no path that gives one provider job two provider-side identities.
func TestProviderJobIdentitySurvivesRedrive(t *testing.T) {
	store := newFakeAccountingStore()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	newRequest := func(job *cstables.TableProviderJob) JobRequest {
		return JobRequest{
			Provider:      schemas.OpenAI,
			ProviderJobID: "batch_325",
			FallbackModel: "gpt-4o-mini",
			ClaimedBy:     "identity-test",
			Now:           now,
			Job:           job,
			Payload: BatchPayload{
				RequestCounts: &schemas.BatchRequestCounts{Total: 1, Completed: 1},
				Results: []schemas.BatchResultItem{
					openAIResult(200, "gpt-4o-mini", 10, 5),
				},
			},
		}
	}

	// First drive: no prior job, so the derived id is minted here.
	_, err := AccountBatchResults(context.Background(), store, store, fakePricing{}, newRequest(nil))
	require.NoError(t, err)
	derived := cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.OpenAI), "batch_325")
	require.Len(t, store.jobs, 1)
	first, ok := store.jobs[derived]
	require.True(t, ok, "the row landed under the derived id")
	createdAt := first.UpdatedAt

	// Second drive WITH the existing row (the sweeper's own read): same identity, no
	// second row, no re-attribution of provider or model.
	existing := *first
	_, err = AccountBatchResults(context.Background(), store, store, fakePricing{}, newRequest(&existing))
	require.NoError(t, err)
	require.Len(t, store.jobs, 1, "a re-drive never mints a second provider-side identity")
	again, ok := store.jobs[derived]
	require.True(t, ok)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.Provider, again.Provider)
	assert.Equal(t, first.JobID, again.JobID)
	assert.Equal(t, first.Model, again.Model)

	// Third drive WITHOUT a prior job (a fresh inline caller): the deterministic
	// derivation recomputes the same id and converges on the same row — the property
	// that lets either side of the mapping recompute the other's key.
	time.Sleep(2 * time.Millisecond) // let UpdatedAt advance if a writer touches the row
	_, err = AccountBatchResults(context.Background(), store, store, fakePricing{}, newRequest(nil))
	require.NoError(t, err)
	require.Len(t, store.jobs, 1)
	converged, ok := store.jobs[derived]
	require.True(t, ok)
	assert.Equal(t, first.ID, converged.ID)
	assert.Equal(t, first.JobID, converged.JobID)
	assert.True(t, !converged.UpdatedAt.Before(createdAt), "convergence updates the row it found, it does not duplicate it")
}
