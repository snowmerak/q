package thinker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	qlibrary "github.com/snowmerak/q/library"
)

const (
	JobCheckpointVersion  = 1
	maximumCheckpointSlot = 1_000_000
)

// JobCheckpointStore persists the write-ahead state for one Thinker learning
// segment. Implementations must replace a saved checkpoint atomically.
type JobCheckpointStore interface {
	LoadThinkerCheckpoint() (JobCheckpoint, bool, error)
	SaveThinkerCheckpoint(JobCheckpoint) error
}

// JobCheckpoint is the durable write-ahead state for a Thinker job. Pending is
// saved before its Library request is sent; acknowledged results are saved
// before the next model round starts.
type JobCheckpoint struct {
	Version      int                       `json:"version"`
	JobID        string                    `json:"job_id"`
	InputDigest  string                    `json:"input_digest"`
	Generation   string                    `json:"generation"`
	NextSlot     int                       `json:"next_slot"`
	Pending      *RegistrationCheckpoint   `json:"pending,omitempty"`
	Acknowledged []AcknowledgedProposition `json:"acknowledged,omitempty"`
	Result       Result                    `json:"result"`
	Completed    bool                      `json:"completed,omitempty"`
}

// RegistrationCheckpoint holds the exact logical request and idempotency key
// that may need to be replayed after an ambiguous Library failure.
type RegistrationCheckpoint struct {
	Slot    int                                 `json:"slot"`
	Key     string                              `json:"key"`
	Request qlibrary.PropositionRegisterRequest `json:"request"`
}

// AcknowledgedProposition is the host-maintained ledger shown to a resumed
// Thinker model so already processed propositions are not generated again.
type AcknowledgedProposition struct {
	Content string `json:"content"`
	ID      string `json:"id,omitempty"`
	Action  string `json:"action"`
}

func newJobCheckpoint(job Job, truncated bool) (JobCheckpoint, error) {
	digest, err := thinkerJobDigest(job)
	if err != nil {
		return JobCheckpoint{}, err
	}
	generation, err := learningStreamID()
	if err != nil {
		return JobCheckpoint{}, fmt.Errorf("thinker: generate checkpoint identity: %w", err)
	}
	return JobCheckpoint{
		Version: JobCheckpointVersion, JobID: job.ID, InputDigest: digest, Generation: generation,
		Result: Result{Truncated: truncated},
	}, nil
}

func thinkerJobDigest(job Job) (string, error) {
	input := struct {
		ID       string           `json:"id"`
		Boundary string           `json:"boundary,omitempty"`
		Messages []client.Message `json:"messages"`
		Refs     []string         `json:"refs,omitempty"`
	}{ID: job.ID, Boundary: job.Boundary, Messages: job.Messages, Refs: job.Refs}
	body, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("thinker: encode checkpoint input identity: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func checkpointIdempotencyKey(checkpoint JobCheckpoint, slot int) string {
	jobSum := sha256.Sum256([]byte(checkpoint.JobID))
	return fmt.Sprintf("thinker-v1/%s/%s/%d", hex.EncodeToString(jobSum[:16]), checkpoint.Generation, slot)
}

// CloneJobCheckpoint returns a deep copy suitable for handing across a storage
// boundary or retaining in a test double.
func CloneJobCheckpoint(checkpoint JobCheckpoint) JobCheckpoint {
	result := checkpoint
	result.Result.IDs = append([]string(nil), checkpoint.Result.IDs...)
	result.Acknowledged = append([]AcknowledgedProposition(nil), checkpoint.Acknowledged...)
	if checkpoint.Pending != nil {
		pending := *checkpoint.Pending
		pending.Request.Queries = append([]string(nil), checkpoint.Pending.Request.Queries...)
		pending.Request.Tags = append([]string(nil), checkpoint.Pending.Request.Tags...)
		pending.Request.Refs = append([]string(nil), checkpoint.Pending.Request.Refs...)
		if checkpoint.Pending.Request.Embeddings != nil {
			embeddings := *checkpoint.Pending.Request.Embeddings
			embeddings.Vectors = make([][]float32, len(checkpoint.Pending.Request.Embeddings.Vectors))
			for index, vector := range checkpoint.Pending.Request.Embeddings.Vectors {
				embeddings.Vectors[index] = append([]float32(nil), vector...)
			}
			pending.Request.Embeddings = &embeddings
		}
		result.Pending = &pending
	}
	return result
}

// ValidateJobCheckpoint rejects corrupt or internally inconsistent durable
// state before it can select an idempotency key or suppress model work.
func ValidateJobCheckpoint(checkpoint JobCheckpoint) error {
	if checkpoint.Version != JobCheckpointVersion {
		return fmt.Errorf("unsupported Thinker checkpoint version %d", checkpoint.Version)
	}
	if strings.TrimSpace(checkpoint.JobID) == "" {
		return errors.New("Thinker checkpoint job ID is required")
	}
	if !validHex(checkpoint.InputDigest, sha256.Size) {
		return errors.New("Thinker checkpoint input digest is invalid")
	}
	if !validHex(checkpoint.Generation, 12) {
		return errors.New("Thinker checkpoint generation is invalid")
	}
	if checkpoint.NextSlot < 0 || checkpoint.NextSlot > maximumCheckpointSlot {
		return errors.New("Thinker checkpoint slot is invalid")
	}
	if checkpoint.Result.Proposed < checkpoint.Result.Processed || checkpoint.Result.Processed != checkpoint.NextSlot ||
		checkpoint.Result.Registered != checkpoint.Result.Created+checkpoint.Result.Merged ||
		checkpoint.Result.Processed != checkpoint.Result.Registered+checkpoint.Result.Discarded ||
		len(checkpoint.Acknowledged) != checkpoint.Result.Processed ||
		len(checkpoint.Result.IDs) > checkpoint.Result.Registered {
		return errors.New("Thinker checkpoint result counters are inconsistent")
	}
	if checkpoint.Completed && checkpoint.Pending != nil {
		return errors.New("completed Thinker checkpoint has a pending registration")
	}
	for _, acknowledged := range checkpoint.Acknowledged {
		if strings.TrimSpace(acknowledged.Content) == "" || !supportedPropositionAction(acknowledged.Action) {
			return errors.New("Thinker checkpoint acknowledgement is invalid")
		}
	}
	if checkpoint.Pending != nil {
		if checkpoint.Pending.Slot != checkpoint.NextSlot || checkpoint.Pending.Key != checkpointIdempotencyKey(checkpoint, checkpoint.NextSlot) ||
			checkpoint.Result.Proposed <= checkpoint.Result.Processed || strings.TrimSpace(checkpoint.Pending.Request.Content) == "" {
			return errors.New("Thinker checkpoint pending registration is invalid")
		}
	}
	return nil
}

func validHex(value string, bytes int) bool {
	if len(value) != bytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}

func supportedPropositionAction(action string) bool {
	switch action {
	case qlibrary.PropositionActionCreate, qlibrary.PropositionActionMerge, qlibrary.PropositionActionDiscard:
		return true
	default:
		return false
	}
}
