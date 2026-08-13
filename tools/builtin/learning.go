package builtin

// LearnInput is intentionally empty. The active q session owns the durable
// conversation cursor and treats the tool call itself as an explicit segment
// boundary.
type LearnInput struct{}

type LearnOutput struct {
	Enqueued bool `json:"enqueued"`
}
