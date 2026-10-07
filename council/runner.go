package council

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/workspace"
)

// Run executes the configured council rounds followed by chair synthesis.
// First opinions remain independent of the current turn's other members.
func Run(ctx context.Context, host SessionRunner, value Council, previous []Turn, turn Turn, roots []string, sessionRoot string, maxParallel int, save func(Turn) error) (Turn, error) {
	return run(ctx, host, value, previous, turn, roots, sessionRoot, maxParallel, save, false)
}

// Resume retries missing or failed work in a saved turn, keeping successful
// outputs from the current round and all earlier rounds.
func Resume(ctx context.Context, host SessionRunner, value Council, previous []Turn, turn Turn, roots []string, sessionRoot string, maxParallel int, save func(Turn) error) (Turn, error) {
	return run(ctx, host, value, previous, turn, roots, sessionRoot, maxParallel, save, true)
}

func run(ctx context.Context, host SessionRunner, value Council, previous []Turn, turn Turn, roots []string, sessionRoot string, maxParallel int, save func(Turn) error, resume bool) (Turn, error) {
	if host == nil {
		return turn, errors.New("council session host is unavailable")
	}
	if err := Validate(value); err != nil {
		return turn, err
	}
	if maxParallel < 1 {
		maxParallel = 3
	}
	if maxParallel > len(value.Members) {
		maxParallel = len(value.Members)
	}
	if value.Scope != Independent && len(roots) == 0 {
		return turn, errors.New("council workspace roots are unavailable")
	}
	legacyResume := resume && len(turn.MemberSessions) == 0
	if err := ensureTurnSessions(&turn, workspace.Store{Root: sessionRoot}, len(value.Members)); err != nil {
		return turn, err
	}
	defer func() {
		for _, id := range append(append([]string(nil), turn.MemberSessions...), turn.ChairSession) {
			_ = host.ReleaseSession(workspace.Store{Root: sessionRoot}, id)
		}
	}()
	totalRounds := EffectiveRounds(value)
	turn.TotalRounds, turn.CurrentRound = totalRounds, 1
	if resume {
		if err := prepareResume(&turn, value); err != nil {
			return turn, err
		}
	} else {
		turn.Rounds = make([]Round, totalRounds)
		turn.Responses = make([]Response, len(value.Members))
	}
	turn.Stage, turn.Status = "opinions", "running"
	turn.Error = ""
	for index, seat := range value.Members {
		if !completeResponse(turn.Responses[index], seat) {
			turn.Responses[index] = Response{Label: label(index), Model: seat.Model}
		}
	}
	turn.Rounds[0] = Round{Number: 1, Responses: turn.Responses}
	if err := save(turn); err != nil {
		return turn, err
	}
	var progressMu sync.Mutex
	var progressErr error
	publish := func(update func()) {
		progressMu.Lock()
		defer progressMu.Unlock()
		update()
		if progressErr == nil {
			progressErr = save(turn)
		}
	}
	semaphore := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for index, seat := range value.Members {
		if completeResponse(turn.Responses[index], seat) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				publish(func() { turn.Responses[index].Error = ctx.Err().Error() })
				return
			}
			defer func() { <-semaphore }()
			answer, err := firstOpinion(ctx, host, seat, turn.Prompt, previous, roots, sessionRoot, turn.MemberSessions[index], "round-1")
			response := Response{Label: label(index), Model: seat.Model, Text: answer}
			if err != nil {
				response.Error = err.Error()
			}
			publish(func() { turn.Responses[index] = response })
		}()
	}
	wg.Wait()
	if progressErr != nil {
		return turn, progressErr
	}
	if err := ctx.Err(); err != nil {
		return turn, err
	}
	valid := append([]Response(nil), turn.Responses...)
	for index, response := range valid {
		if !completeResponse(response, value.Members[index]) {
			return turn, fmt.Errorf("council member %s did not produce an answer: %s", label(index), response.Error)
		}
	}
	for number := 2; number <= totalRounds; number++ {
		turn.Stage, turn.CurrentRound = "reviews", number
		if len(turn.Rounds[number-1].Reviews) != len(value.Members) {
			turn.Rounds[number-1].Reviews = make([]Review, len(value.Members))
		}
		turn.Rounds[number-1].Number = number
		for index, seat := range value.Members {
			if !completeReview(turn.Rounds[number-1].Reviews[index], seat) {
				turn.Rounds[number-1].Reviews[index] = Review{Model: seat.Model}
			}
		}
		turn.Reviews, turn.Ranking = turn.Rounds[number-1].Reviews, nil
		if err := save(turn); err != nil {
			return turn, err
		}
		feedback := formatPriorFeedback(turn.Rounds[number-2:number-1], value.Members)
		for index, seat := range value.Members {
			if completeReview(turn.Reviews[index], seat) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case semaphore <- struct{}{}:
				case <-ctx.Done():
					publish(func() { turn.Reviews[index].Error = ctx.Err().Error() })
					return
				}
				defer func() { <-semaphore }()
				var peers []Response
				var own Response
				for _, response := range valid {
					if response.Model == seat.Model {
						own = response
					} else {
						peers = append(peers, response)
					}
				}
				if len(peers) == 0 {
					publish(func() { turn.Reviews[index].Error = "no peer answers to review" })
					return
				}
				system := "You are an anonymous peer reviewer. Write a concise, balanced review of the supplied peer answers. Identify factual errors, unsupported claims, useful insights, and consequential disagreements. Explain what should change in the council's conclusion. Write plain text, not JSON. Do not rank or score answers. Do not identify any model."
				user := "Peer answers:\n" + formatResponses(anonymizeResponses(peers, value.Members))
				if legacyResume {
					user = "Question: " + turn.Prompt + "\n\n" + user
				}
				if number >= 3 {
					system = "You are continuing a multi-round LLM council. Re-evaluate the anonymized answers in light of previous reviews. Write an integrated plain-text review: resolve supported criticisms, preserve meaningful disagreements, and state what the chair should conclude. Focus on new findings or changed judgments instead of repeating the full answers or earlier reviews. Do not rank or score answers. Do not identify any model."
					user = "New anonymous reviews from the previous round:\n" + feedback
					if legacyResume {
						own = anonymizeResponses([]Response{own}, value.Members)[0]
						user = "Question: " + turn.Prompt + "\nPeer answers:\n" + formatResponses(anonymizeResponses(peers, value.Members)) + "\nYour first answer:\n" + own.Text + "\nPrevious anonymous reviews:\n" + formatPriorFeedback(turn.Rounds[:number-1], value.Members)
					}
				}
				answer, err := runSeat(ctx, host, seat, system+"\n\n"+user, roots, sessionRoot, turn.MemberSessions[index], fmt.Sprintf("round-%d", number))
				review := Review{Model: seat.Model, Text: answer}
				if err != nil {
					review.Error = err.Error()
				}
				publish(func() {
					turn.Reviews[index] = review
				})
			}()
		}
		wg.Wait()
		if progressErr != nil {
			return turn, progressErr
		}
		if err := ctx.Err(); err != nil {
			return turn, err
		}
		for index, review := range turn.Reviews {
			if !completeReview(review, value.Members[index]) {
				return turn, fmt.Errorf("council round %d reviewer %s failed: %s", number, label(index), review.Error)
			}
		}
	}
	turn.Stage = "synthesis"
	if err := save(turn); err != nil {
		return turn, err
	}
	answer, err := runSeat(ctx, host, value.Chair,
		"Question: "+turn.Prompt+"\n\nAll council rounds:\n"+formatChairHistory(turn.Rounds, value.Members),
		roots, sessionRoot, turn.ChairSession, "synthesis")
	if err != nil {
		return turn, err
	}
	turn.Final, turn.Stage, turn.Status = answer, "complete", "completed"
	if err := save(turn); err != nil {
		return turn, err
	}
	return turn, nil
}

func completeResponse(response Response, seat Seat) bool {
	return response.Model == seat.Model && response.Error == "" && strings.TrimSpace(response.Text) != ""
}

func completeReview(review Review, seat Seat) bool {
	return review.Model == seat.Model && review.Error == "" && strings.TrimSpace(review.Text) != ""
}

func prepareResume(turn *Turn, value Council) error {
	count := EffectiveRounds(value)
	stored := turn.Rounds
	turn.Rounds = make([]Round, count)
	copy(turn.Rounds, stored)
	for number := 1; number <= count; number++ {
		turn.Rounds[number-1].Number = number
	}
	if len(turn.Rounds[0].Responses) != len(value.Members) {
		turn.Rounds[0].Responses = append([]Response(nil), turn.Responses...)
	}
	responses := make([]Response, len(value.Members))
	copy(responses, turn.Rounds[0].Responses)
	turn.Responses = responses
	turn.Rounds[0].Responses = responses
	firstIncomplete := count + 1
	for index, seat := range value.Members {
		if !completeResponse(responses[index], seat) {
			firstIncomplete = 1
		}
	}
	for number := 2; number <= count; number++ {
		reviews := turn.Rounds[number-1].Reviews
		if len(reviews) != len(value.Members) && number == 2 && len(turn.Reviews) == len(value.Members) && len(stored) == 0 {
			reviews = turn.Reviews
			turn.Rounds[number-1].Reviews = reviews
		}
		if len(reviews) != len(value.Members) {
			firstIncomplete = min(firstIncomplete, number)
			continue
		}
		for index, seat := range value.Members {
			if !completeReview(reviews[index], seat) {
				firstIncomplete = min(firstIncomplete, number)
			}
		}
	}
	if firstIncomplete == count+1 && strings.TrimSpace(turn.Final) != "" {
		return errors.New("council turn is already complete")
	}
	for number := firstIncomplete + 1; number <= count; number++ {
		turn.Rounds[number-1] = Round{Number: number}
	}
	turn.Final = ""
	return nil
}

func label(index int) string { return string(rune('A' + index)) }

func formatResponses(responses []Response) string {
	var result strings.Builder
	for _, response := range responses {
		fmt.Fprintf(&result, "Answer %s:\n%s\n\n", response.Label, response.Text)
	}
	return result.String()
}

func anonymizeResponses(responses []Response, members []Seat) []Response {
	result := make([]Response, len(responses))
	copy(result, responses)
	for index := range result {
		result[index].Model = ""
		result[index].Text = redactModelNames(result[index].Text, members)
	}
	return result
}

func redactModelNames(value string, members []Seat) string {
	for _, member := range members {
		value = strings.ReplaceAll(value, member.Model, "[model identity]")
	}
	return value
}

func reviewJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	return strings.TrimSpace(trimmed)
}

// ReviewAnalysis strips the repeated revised_answer from older raw JSON reviews.
// Plain-text reviews are returned unchanged.
func ReviewAnalysis(raw string) string {
	var parsed struct {
		Analysis string `json:"analysis"`
	}
	if json.Unmarshal([]byte(reviewJSON(raw)), &parsed) == nil && strings.TrimSpace(parsed.Analysis) != "" {
		return strings.TrimSpace(parsed.Analysis)
	}
	return raw
}

func formatPriorFeedback(rounds []Round, members []Seat) string {
	var result strings.Builder
	for _, round := range rounds {
		if round.Number < 2 {
			continue
		}
		fmt.Fprintf(&result, "Round %d reviews:\n", round.Number)
		for index, review := range round.Reviews {
			if review.Error != "" || review.Text == "" {
				continue
			}
			fmt.Fprintf(&result, "Reviewer %d: %s\n", index+1, redactModelNames(ReviewAnalysis(review.Text), members))
		}
	}
	return result.String()
}

func formatChairHistory(rounds []Round, members []Seat) string {
	var result strings.Builder
	for _, round := range rounds {
		fmt.Fprintf(&result, "Round %d:\n", round.Number)
		if len(round.Responses) > 0 {
			var valid []Response
			for _, response := range round.Responses {
				if response.Error == "" && strings.TrimSpace(response.Text) != "" {
					valid = append(valid, response)
				}
			}
			result.WriteString(formatResponses(anonymizeResponses(valid, members)))
		}
		if round.Number >= 2 {
			result.WriteString(formatPriorFeedback([]Round{round}, members))
		}
	}
	return result.String()
}

func priorContext(previous []Turn) string {
	var result strings.Builder
	start := len(previous) - 8
	if start < 0 {
		start = 0
	}
	for _, turn := range previous[start:] {
		if turn.Status == "completed" {
			fmt.Fprintf(&result, "User: %s\nCouncil: %s\n\n", turn.Prompt, turn.Final)
		}
	}
	return result.String()
}

func firstOpinion(ctx context.Context, host SessionRunner, seat Seat, prompt string, previous []Turn, roots []string, sessionRoot, sessionID, operationID string) (string, error) {
	user := prompt
	if context := priorContext(previous); context != "" {
		user = "Previous council conversation:\n" + context + "Current question:\n" + prompt
	}
	system := "Provide an independent, evidence-conscious first opinion. Other council members' current answers are unavailable. Do not identify your model."
	if len(roots) > 0 {
		system = "Provide an independent first opinion. Investigate the available repositories using read-only tools. Never modify source files. Cite relevant paths and acknowledge uncertainty. Do not identify your model. If you start a task, put your complete opinion and evidence in task_complete.summary."
	}
	return runSeat(ctx, host, seat, system+"\n\n"+user, roots, sessionRoot, sessionID, operationID)
}

// SessionRunner is the existing Studio session execution boundary. Council
// coordinates turns; Q owns the complete context, tool loop and durable state.
type SessionRunner interface {
	RunWithOptions(context.Context, workspace.Store, string, string, app.SessionOptions, app.SessionEventSink) error
	ReleaseSession(workspace.Store, string) error
}

const memberInstructions = "You participate in an LLM council. Follow each user turn's stage instructions: first provide your independent opinion, then review anonymous peer answers and integrate later feedback. Write evidence-conscious plain-text conclusions. Do not rank or score answers or identify any model. Council mode is read-only: only the advertised read tools are authorized. Do not request edits, commands, or delegation. If you use task_complete, include your complete answer in its summary."
const chairInstructions = "You are the chair of an LLM council. Consider independent answers and all rounds of peer reviews. Synthesize supported findings, corrections, meaningful disagreements and uncertainty. Do not claim agreement proves correctness. Answer directly without repetition. Council mode is read-only; only advertised read tools are authorized."

func runSeat(ctx context.Context, host SessionRunner, seat Seat, prompt string, roots []string, sessionRoot, sessionID, operationID string) (string, error) {
	system := memberInstructions
	if operationID == "synthesis" {
		system = chairInstructions
	}
	options := app.SessionOptions{
		Model: seat.Model, ReasoningEffort: seat.ReasoningEffort, SystemPrompt: system,
		DisableLearning: true, RuntimeKey: "council-read-only-v1", OperationID: operationID,
		RuntimeFactory: readRuntimeFactory(roots),
	}
	if len(roots) > 0 {
		options.WorkingDirectory = roots[0]
		options.AuxiliaryDirectories = append([]string(nil), roots[1:]...)
	}
	// Stage instructions are a new user turn; the system and tool prefix remains
	// unchanged throughout the member's session.
	var result string
	err := host.RunWithOptions(ctx, workspace.Store{Root: sessionRoot}, sessionID, prompt, options, func(event app.SessionEvent) error {
		if event.Type == "result" {
			result = event.Content
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("model %s request failed: %w", seat.Model, err)
	}
	if strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("model %s returned empty answer", seat.Model)
	}
	return result, nil
}

func ensureTurnSessions(turn *Turn, store workspace.Store, members int) error {
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		return err
	}
	if len(turn.MemberSessions) != 0 && len(turn.MemberSessions) != members {
		return errors.New("invalid council member sessions")
	}
	if len(turn.MemberSessions) == 0 {
		turn.MemberSessions = make([]string, members)
	}
	create := func(id *string) error {
		if *id != "" {
			_, err := store.ForSession(*id)
			return err
		}
		selected, lock, err := workspace.CreateSession(store.Root, "q studio council")
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := selected.Save(workspace.Session{}); err != nil {
			return err
		}
		*id = selected.SessionID
		return nil
	}
	for index := range turn.MemberSessions {
		if err := create(&turn.MemberSessions[index]); err != nil {
			return err
		}
	}
	return create(&turn.ChairSession)
}

func NewTurn(value Council, prompt string) (Turn, error) {
	if strings.TrimSpace(prompt) == "" {
		return Turn{}, errors.New("question is required")
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		return Turn{}, err
	}
	now := time.Now().UTC()
	return Turn{ID: id, CouncilID: value.ID, Prompt: strings.TrimSpace(prompt), Members: append([]Seat(nil), value.Members...), Chair: value.Chair, TotalRounds: EffectiveRounds(value), Status: "queued", Stage: "queued", CreatedAt: now, UpdatedAt: now}, nil
}
