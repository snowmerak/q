package commitagent

import (
	"reflect"
	"testing"
)

func TestHeadlessSessionIncludesSingleCommitFiles(t *testing.T) {
	workflow := &HeadlessSession{session: &Session{
		state:    repositoryState{files: []string{"app/acp.go", "README.md"}, autoStaged: true},
		proposal: proposalState{Single: &Proposal{Type: "feat", Scope: "acp", Summary: "add commit workflow"}},
	}}
	proposals := workflow.Proposals()
	if len(proposals) != 1 || !reflect.DeepEqual(proposals[0].Files, []string{"app/acp.go", "README.md"}) ||
		!workflow.AutoStaged() {
		t.Fatalf("proposals = %#v, auto staged = %v", proposals, workflow.AutoStaged())
	}
}
