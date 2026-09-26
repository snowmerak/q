package app

import (
	"context"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	qlsp "github.com/snowmerak/q/lsp"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/thinker"
	"github.com/snowmerak/q/workspace"
)

type model struct {
	hostState
	lifecycleState
	sessionState
	providerState
	modelSettingsState
	loomState
	ignoreState
	helpState
	skillsState
	lspState
	mcpState
	agentsState
	chatState
}

// hostState owns one copyable part of the Bubble Tea model.
type hostState struct {
	custom        customManager
	ctx           context.Context
	store         config.Store
	factory       clientFactory
	screen        screen
	width         int
	height        int
	dark          bool
	status        string
	runtime       providerRuntime
	toolRuntime   agentToolRuntime
	libraryClient *qlibrary.Client
}

// lifecycleState owns one copyable part of the Bubble Tea model.
type lifecycleState struct {
	thinkerSerial     *thinker.Serial
	learning          *thinker.Machine
	learningCtx       context.Context
	learningCancel    context.CancelFunc
	thinkerBusy       bool
	thinkerJobID      string
	sessionGeneration uint64
}

// sessionState owns one copyable part of the Bubble Tea model.
type sessionState struct {
	workspaceStore            *workspace.Store
	workspaceLock             *workspace.Lock
	sessions                  []workspace.SessionEntry
	sessionCursor             int
	sessionDeleteID           string
	sessionPickerRequired     bool
	workspaceRestored         bool
	delegationRecoveryPending bool
	recoverDelegationTurn     bool
	workspaceModel            workspace.ModelConfig
	workspaceModelRestored    bool
	workspaceLearning         workspace.LearningConfig
	workspaceLearningRestored bool
	archive                   recordArchive
	archiveSearch             *archiveembed.Archive
	archiveErr                error
	runID                     string
	sessionTitle              string
	sessionUpdatedAt          time.Time
	standalone                bool
	standaloneRoot            screen
}

// providerState owns one copyable part of the Bubble Tea model.
type providerState struct {
	setup                 [setupFieldCount]textinput.Model
	setupFocus            int
	setupEdit             bool
	providerEditIndex     int
	providerAdding        bool
	providerTypeCursor    int
	providerKindCursor    int
	gatewayConfig         gateway.Config
	gatewayConfigOnly     bool
	providerCursor        int
	providerReturn        screen
	gatewaySettingsStore  gatewayconfig.Store
	gatewaySettings       gatewayconfig.Config
	gatewayCursor         int
	gatewayHostInput      textinput.Model
	gatewayPortInput      textinput.Model
	gatewayNetworkFocus   int
	gatewayKeyCursor      int
	gatewayKeyAlias       textinput.Model
	gatewayKeyAdding      bool
	gatewayKeyRevokeArmed bool
	generatedGatewayKey   string
	librarySettingsStore  qlibrary.ConfigStore
	librarySettings       qlibrary.Config
	libraryHostInput      textinput.Model
	libraryPortInput      textinput.Model
	libraryNetworkFocus   int
}

// modelSettingsState owns one copyable part of the Bubble Tea model.
type modelSettingsState struct {
	discovering               bool
	models                    []client.Model
	modelCursor               int
	modelFilter               textinput.Model
	modelPickerStage          modelPickerStage
	modelChooseTarget         bool
	modelTarget               string
	modelTargetCursor         int
	modelScopeCursor          int
	modelWorkspace            bool
	modelRoleNameInput        textinput.Model
	modelRoleDeleteArmed      bool
	reasoningCursor           int
	modelSelection            client.Model
	embeddingDimensions       textinput.Model
	modelContextWindow        textinput.Model
	modelGroupCursor          int
	modelGroupName            string
	modelGroupNameInput       textinput.Model
	modelGroupDraft           config.ModelGroupConfig
	modelGroupCandidate       config.ModelCandidateConfig
	modelGroupCandidateCursor int
	modelGroupCandidateEdit   int
	modelGroupTimeoutInput    textinput.Model
	modelGroupDeleteArmed     bool
	draftConfig               config.Config
	modelReturn               screen
}

// loomState owns one copyable part of the Bubble Tea model.
type loomState struct {
	loomInputs        [5]textinput.Model
	loomFocus         int
	loomDraft         config.LoomConfig
	loomStats         loom.Stats
	loomBusy          bool
	loomExitAfterSave bool
}

// ignoreState owns one copyable part of the Bubble Tea model.
type ignoreState struct {
	ignoreEditor       textarea.Model
	ignoreOriginal     string
	ignoreDiscardArmed bool
}

// helpState owns one copyable part of the Bubble Tea model.
type helpState struct {
	helpReturn screen
}

// skillsState owns one copyable part of the Bubble Tea model.
type skillsState struct {
	skillsBusy        bool
	skillsStatusError bool
	skillsScope       int
	skillsCursor      [2]int
	skillsMode        skillScreenMode
	skillsInput       textinput.Model
	skillRegistry     skillRuntime
}

// lspState owns one copyable part of the Bubble Tea model.
type lspState struct {
	lspPanel             int
	lspCursor            [2]int
	lspMode              lspScreenMode
	lspEditID            string
	lspEditingIndex      int
	lspFormFocus         int
	lspInputs            [4]textinput.Model
	lspDraftGlobal       qlsp.GlobalConfig
	lspOriginalGlobal    qlsp.GlobalConfig
	lspDraftWorkspace    qlsp.WorkspaceConfig
	lspOriginalWorkspace qlsp.WorkspaceConfig
	lspDiscardArmed      bool
	lspBusy              bool
	lspDiscoveryID       uint64
}

// mcpState owns one copyable part of the Bubble Tea model.
type mcpState struct {
	mcpSettingsStore mcpconfig.Store
	mcpDraft         mcpconfig.Config
	mcpOriginal      mcpconfig.Config
	mcpPanel         int
	mcpCursor        [2]int
	mcpMode          mcpScreenMode
	mcpEditID        string
	mcpFormFocus     int
	mcpInputs        [6]textinput.Model
	mcpDiscardArmed  bool
	mcpBusy          bool
}

// agentsState owns one copyable part of the Bubble Tea model.
type agentsState struct {
	agentsDraft     config.Config
	agentsCursor    [2]int
	agentsMode      agentsScreenMode
	agentsEditID    string
	agentsFormFocus int
	agentsInputs    [6]textinput.Model
	agentsBusy      bool
	agentsProbe     map[string]string
}

// chatState owns one copyable part of the Bubble Tea model.
type chatState struct {
	config               config.Config
	client               chatClient
	messages             []client.Message
	memory               *memory.Manager
	conversationID       string
	loopMode             string
	activeTask           *workspace.ActiveTask
	pendingMessage       client.Message
	requestEstimate      int
	compactionTarget     int
	input                textarea.Model
	slashCompletion      slashCompletionState
	viewport             viewport.Model
	questionViewport     viewport.Model
	agentTraceViewport   viewport.Model
	helpViewport         viewport.Model
	changes              changesViewState
	spinner              spinner.Model
	commitRunning        bool
	initializing         bool
	startup              tea.Cmd
	waiting              bool
	compacting           bool
	submitPending        bool
	asking               bool
	pendingQuestion      askToUserInput
	questionAnswer       chan<- askToUserOutput
	questionEvents       <-chan agentEvent
	questionTurnID       uint64
	questionChoice       int
	planArmed            bool
	planResumePending    bool
	planCheckpoint       subagent.ExecutionCheckpoint
	agentActivities      []agentActivity
	agentStates          map[string]string
	agentLogVisible      int
	agentTraces          []agentTrace
	agentTraceExpanded   bool
	transcriptThoughts   []transcriptThought
	streamResponse       string
	turnContext          context.Context
	turnCancel           context.CancelFunc
	turnID               uint64
	turnMessageStart     int
	toolResultsCollapsed bool
}
