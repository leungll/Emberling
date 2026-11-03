package mediaresult

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// boundReviewFact is a review fact that binds its review model, as reviews do once they
// may run on a vision model.
func boundReviewFact(photoID, assetRef, policy, reviewModelID string, passed bool) domain.ExecutionFact {
	fact := reviewFact(photoID, assetRef, policy, passed)
	fact.Binding = json.RawMessage(fmt.Sprintf(`{"photoAssetId":%q,"policyVersion":%q,"reviewModelId":%q,"styleDigest":"sha256:style"}`, photoID, policy, reviewModelID))
	return fact
}

func TestMediaResult_MockAndModelReviewsMixed_Rejected(t *testing.T) {
	generated, _ := happyFacts()
	reviewed := []domain.ExecutionFact{
		boundReviewFact("photo_a", "gen_photo_a", "mock-review-policy-v1", "mock", true),
		boundReviewFact("photo_b", "gen_photo_b", "vision-review-policy-v2", "vision-model", true),
		boundReviewFact("photo_c", "gen_photo_c", "vision-review-policy-v2", "vision-model", true),
	}
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonPolicyMismatch, "")
}

func TestMediaResult_SamePolicyDifferentReviewModels_Rejected(t *testing.T) {
	generated, _ := happyFacts()
	reviewed := []domain.ExecutionFact{
		boundReviewFact("photo_a", "gen_photo_a", "vision-review-policy-v2", "vision-model-a", true),
		boundReviewFact("photo_b", "gen_photo_b", "vision-review-policy-v2", "vision-model-b", true),
		boundReviewFact("photo_c", "gen_photo_c", "vision-review-policy-v2", "vision-model-a", true),
	}
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	wantRejected(t, got, reasonPolicyMismatch, "")
}

func TestMediaResult_AllReviewsByOneModel_Accepted(t *testing.T) {
	generated, _ := happyFacts()
	reviewed := []domain.ExecutionFact{
		boundReviewFact("photo_a", "gen_photo_a", "vision-review-policy-v2", "vision-model", true),
		boundReviewFact("photo_b", "gen_photo_b", "vision-review-policy-v2", "vision-model", true),
		boundReviewFact("photo_c", "gen_photo_c", "vision-review-policy-v2", "vision-model", false),
	}
	got, _ := runNode(t, finalValue(t, testDigest, testPhotos...), generated, reviewed)
	if !got.Accepted || got.PassedCount != 2 || got.PolicyVersion != "vision-review-policy-v2" {
		t.Fatalf("summary = %+v, want accepted with two passes under the vision policy", got)
	}
}
