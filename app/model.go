package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/thinker"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
	"golang.org/x/text/unicode/norm"
)

type screen uint8

const (
	screenSetup screen = iota
	screenProviders
	screenModels
	screenLoom
	screenIgnore
	screenSkills
	screenLSP
	screenMCP
	screenAgents
	screenGateway
	screenGatewayNetwork
	screenGatewayKeys
	screenLibrary
	screenHelp
	screenSessions
	screenChat
)

type modelPickerStage uint8

const (
	modelPickerModels modelPickerStage = iota
	modelPickerTargets
	modelPickerReasoning
	modelPickerEmbeddingDimensions
	modelPickerContextWindow
	modelPickerGroups
	modelPickerGroupName
	modelPickerGroupCandidates
	modelPickerGroupCandidateModels
	modelPickerGroupCandidateReasoning
	modelPickerGroupCandidateTimeout
)

const (
	loomAutoGC = iota
	loomMaximumArtifact
	loomMaximumStore
	loomGCTrigger
	loomGCTarget
	loomGCGrace
	loomDryRun
	loomCollect
	loomControlCount
)

const modelGroupChoicePrefix = "group:"

const (
	defaultModelTarget   = "default"
	embeddingModelTarget = "embedding"
)

const (
	modelScopeGlobal = iota
	modelScopeWorkspace
)

const (
	setupProviderID = iota
	setupProviderPrefix
	setupProviderType
	setupProviderKind
	setupBaseURL
	setupAPIKeyEnv
	setupAPIKey
	setupFieldCount
)

type providerTypeOption struct {
	value       string
	label       string
	description string
	baseURL     string
	apiKeyEnv   string
	baseURLHint string
	apiKeyHint  string
}

type providerKindOption struct {
	value       string
	label       string
	description string
}

var providerTypeOptions = []providerTypeOption{
	{
		value: "openai-compatible", label: "OpenAI compatible",
		description: "OpenAI-compatible HTTP API, including local servers",
		baseURL:     "https://api.openai.com/v1", apiKeyEnv: "OPENAI_API_KEY",
		baseURLHint: "https://api.openai.com/v1", apiKeyHint: "OPENAI_API_KEY",
	},
	{
		value: "openrouter", label: "OpenRouter",
		description: "OpenRouter API with native reasoning and cache controls",
		apiKeyEnv:   "OPENROUTER_API_KEY", baseURLHint: "default: https://openrouter.ai/api/v1", apiKeyHint: "OPENROUTER_API_KEY",
	},
	{
		value: "xai", label: "xAI",
		description: "xAI API for Grok models",
		apiKeyEnv:   "XAI_API_KEY", baseURLHint: "default: https://api.x.ai/v1", apiKeyHint: "XAI_API_KEY",
	},
	{
		value: "anthropic", label: "Anthropic",
		description: "Native Anthropic Messages API",
		apiKeyEnv:   "ANTHROPIC_API_KEY", baseURLHint: "optional; uses the Anthropic default", apiKeyHint: "ANTHROPIC_API_KEY",
	},
	{
		value: "codex", label: "Codex App Server",
		description: "Local codex app-server using the current Codex login",
		baseURLHint: "not used", apiKeyHint: "not used",
	},
}

type model struct {
	ctx               context.Context
	store             config.Store
	factory           clientFactory
	screen            screen
	width             int
	height            int
	dark              bool
	status            string
	runtime           providerRuntime
	toolRuntime       agentToolRuntime
	libraryClient     *qlibrary.Client
	thinkerSerial     *thinker.Serial
	learning          *thinker.Machine
	learningCtx       context.Context
	learningCancel    context.CancelFunc
	thinkerBusy       bool
	thinkerJobID      string
	sessionGeneration uint64

	workspaceStore            *workspace.Store
	workspaceLock             *workspace.Lock
	sessions                  []workspace.SessionEntry
	sessionCursor             int
	sessionPickerRequired     bool
	workspaceRestored         bool
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

	setup                     [setupFieldCount]textinput.Model
	setupFocus                int
	setupEdit                 bool
	providerEditIndex         int
	providerAdding            bool
	providerTypeCursor        int
	providerKindCursor        int
	gatewayConfig             gateway.Config
	gatewayConfigOnly         bool
	providerCursor            int
	providerReturn            screen
	gatewaySettingsStore      gatewayconfig.Store
	gatewaySettings           gatewayconfig.Config
	gatewayCursor             int
	gatewayHostInput          textinput.Model
	gatewayPortInput          textinput.Model
	gatewayNetworkFocus       int
	gatewayKeyCursor          int
	gatewayKeyAlias           textinput.Model
	gatewayKeyAdding          bool
	gatewayKeyRevokeArmed     bool
	generatedGatewayKey       string
	librarySettingsStore      qlibrary.ConfigStore
	librarySettings           qlibrary.Config
	libraryHostInput          textinput.Model
	libraryPortInput          textinput.Model
	libraryNetworkFocus       int
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
	loomInputs                [5]textinput.Model
	loomFocus                 int
	loomDraft                 config.LoomConfig
	loomStats                 loom.Stats
	loomBusy                  bool
	loomExitAfterSave         bool
	ignoreEditor              textarea.Model
	ignoreOriginal            string
	ignoreDiscardArmed        bool
	helpReturn                screen
	skillsBusy                bool
	skillsStatusError         bool
	skillsScope               int
	skillsCursor              [2]int
	skillsMode                skillScreenMode
	skillsInput               textinput.Model
	skillRegistry             skillRuntime
	lspPanel                  int
	lspCursor                 [2]int
	lspMode                   lspScreenMode
	lspEditID                 string
	lspEditingIndex           int
	lspFormFocus              int
	lspInputs                 [4]textinput.Model
	lspDraftGlobal            qlsp.GlobalConfig
	lspOriginalGlobal         qlsp.GlobalConfig
	lspDraftWorkspace         qlsp.WorkspaceConfig
	lspOriginalWorkspace      qlsp.WorkspaceConfig
	lspDiscardArmed           bool
	lspBusy                   bool
	lspDiscoveryID            uint64
	mcpSettingsStore          mcpconfig.Store
	mcpDraft                  mcpconfig.Config
	mcpOriginal               mcpconfig.Config
	mcpPanel                  int
	mcpCursor                 [2]int
	mcpMode                   mcpScreenMode
	mcpEditID                 string
	mcpFormFocus              int
	mcpInputs                 [6]textinput.Model
	mcpDiscardArmed           bool
	mcpBusy                   bool
	agentsDraft               config.Config
	agentsOriginal            config.Config
	agentsPanel               int
	agentsCursor              [2]int
	agentsMode                agentsScreenMode
	agentsEditID              string
	agentsFormFocus           int
	agentsInputs              [6]textinput.Model
	agentsDiscardArmed        bool
	agentsBusy                bool
	agentsProbe               map[string]string

	config             config.Config
	client             chatClient
	messages           []client.Message
	memory             *memory.Manager
	conversationID     string
	activeTask         *workspace.ActiveTask
	pendingMessage     client.Message
	requestEstimate    int
	compactionTarget   int
	input              textarea.Model
	slashCompletion    slashCompletionState
	viewport           viewport.Model
	questionViewport   viewport.Model
	agentTraceViewport viewport.Model
	helpViewport       viewport.Model
	spinner            spinner.Model
	commitRunning      bool
	initializing       bool
	startup            tea.Cmd
	waiting            bool
	compacting         bool
	submitPending      bool
	asking             bool
	pendingQuestion    askToUserInput
	questionAnswer     chan askToUserOutput
	questionEvents     <-chan agentEvent
	questionTurnID     uint64
	questionChoice     int
	planArmed          bool
	planResumePending  bool
	planCheckpoint     subagent.ExecutionCheckpoint
	agentActivities    []agentActivity
	agentStates        map[string]string
	agentLogVisible    int
	agentTraces        []agentTrace
	agentTraceExpanded bool
	transcriptThoughts []transcriptThought
	streamResponse     string
	turnContext        context.Context
	turnCancel         context.CancelFunc
	turnID             uint64
	turnMessageStart   int

	toolResultsCollapsed bool
}

type configuredMsg struct {
	config          config.Config
	client          chatClient
	preserveHistory bool
	err             error
}

type archiveEmbeddingConfiguredMsg struct {
	stats archiveembed.BackfillStats
	err   error
}

type mcpSettingsSavedMsg struct {
	config   mcpconfig.Config
	statuses []qtools.ExternalStatus
	err      error
}

type agentsSettingsSavedMsg struct {
	config config.Config
	err    error
}

type agentConnectionProbedMsg struct {
	id  string
	err error
}

type modelTargetConfiguredMsg struct {
	config config.Config
	target string
	err    error
}

type workspaceModelConfiguredMsg struct {
	config  workspace.ModelConfig
	target  string
	cleared bool
	err     error
}

type chatResultMsg struct {
	turnID          uint64
	response        *client.ChatResponse
	intermediate    []client.Message
	requestEstimate int
	toolCalls       int
	err             error
}

type agentEvent struct {
	status          string
	activity        *agentActivity
	trace           *agentTrace
	plan            *agentPlanUpdate
	message         *client.Message
	call            *client.ToolCall
	question        *askToUserInput
	answer          chan askToUserOutput
	toolIsError     bool
	response        *client.ChatResponse
	requestEstimate int
	toolCalls       int
	learningName    string
	learningPayload json.RawMessage
	streamDelta     *chatStreamDelta
	taskStarted     *workspace.ActiveTask
	taskCompleted   bool
	err             error
}

type agentPlanUpdate struct {
	Entries []agentPlanEntry
}

type agentPlanEntry struct {
	Content string
	Status  string
}

const (
	agentPlanPending    = "pending"
	agentPlanInProgress = "in_progress"
	agentPlanCompleted  = "completed"
)

func agentPlanStatus(update agentPlanUpdate) string {
	for index, entry := range update.Entries {
		if entry.Status == agentPlanInProgress {
			return fmt.Sprintf("Plan task %d/%d · %s", index+1, len(update.Entries), entry.Content)
		}
	}
	if len(update.Entries) > 0 {
		return fmt.Sprintf("Plan tasks completed · %d/%d", len(update.Entries), len(update.Entries))
	}
	return "Plan updated"
}

type agentActivity struct {
	Agent    string
	TaskID   string
	ParentID string
	Action   string
	Detail   string
}

type agentTrace struct {
	Agent    string
	TaskID   string
	ParentID string
	Kind     string
	Name     string
	Content  string
	IsError  bool
}

type agentEventMsg struct {
	events <-chan agentEvent
	event  agentEvent
	turnID uint64
}

type compactionResultMsg struct {
	turnID   uint64
	response *client.ChatResponse
	plan     memory.Plan
	err      error
}

type deferredSubmitMsg struct{}

type loomStatsMsg struct {
	stats loom.Stats
	err   error
}

type thinkerResultMsg struct {
	jobID             string
	sessionGeneration uint64
	result            thinker.Result
	err               error
}

type loomActionMsg struct {
	config config.Config
	stats  loom.Stats
	result *loom.GCResult
	action string
	err    error
}

const imeCommitGracePeriod = 35 * time.Millisecond

type modelsResultMsg struct {
	models []client.Model
	config config.Config
	err    error
}

type providersAppliedMsg struct {
	models        []client.Model
	config        config.Config
	gatewayConfig gateway.Config
	client        chatClient
	modelTarget   string
	openModels    bool
	replaceClient bool
	warning       string
	err           error
}

type modelGroupsConfiguredMsg struct {
	config config.Config
	group  string
	err    error
}

func newModel(ctx context.Context, store config.Store, factory clientFactory) model {
	return newManagedModel(ctx, store, factory, nil)
}

func newManagedModel(ctx context.Context, store config.Store, factory clientFactory, runtime providerRuntime) model {
	defaults := config.Default()
	m := model{
		ctx: ctx, store: store, factory: factory, runtime: runtime, screen: screenSetup,
		viewport:           viewport.New(viewport.WithWidth(80), viewport.WithHeight(12)),
		questionViewport:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(8)),
		agentTraceViewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(8)),
		helpViewport:       viewport.New(viewport.WithWidth(80), viewport.WithHeight(12)),
		spinner:            spinner.New(spinner.WithSpinner(spinner.Dot)),
		thinkerSerial:      &thinker.Serial{},
	}
	m.resetSessionLearning()
	if runtime != nil {
		m.gatewayConfig = runtime.Config()
	}
	m.viewport.SoftWrap = true
	m.questionViewport.SoftWrap = true
	m.agentTraceViewport.SoftWrap = true
	m.helpViewport.SoftWrap = true
	m.questionViewport.FillHeight = true
	m.agentTraceViewport.FillHeight = true
	m.helpViewport.FillHeight = true

	placeholders := []string{
		"provider id",
		"optional; defaults to provider id",
		"",
		"",
		"https://api.openai.com/v1",
		"OPENAI_API_KEY",
		"optional inline key",
	}
	values := []string{
		"default",
		"",
		"",
		"",
		defaults.Provider.BaseURL,
		defaults.Provider.APIKeyEnv,
		defaults.Provider.APIKey,
	}
	for index := range m.setup {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = placeholders[index]
		field.SetValue(values[index])
		field.SetWidth(60)
		m.setup[index] = field
	}
	m.setup[setupAPIKey].EchoMode = textinput.EchoPassword
	m.setProviderType("openai-compatible")
	m.setup[m.setupFocus].Focus()
	m.modelFilter = textinput.New()
	m.modelFilter.Prompt = "/ "
	m.modelFilter.Placeholder = "Filter models"
	m.modelFilter.SetWidth(60)
	m.embeddingDimensions = textinput.New()
	m.embeddingDimensions.Prompt = "dimensions · "
	m.embeddingDimensions.Placeholder = "1536"
	m.embeddingDimensions.CharLimit = 5
	m.embeddingDimensions.SetWidth(24)
	m.modelContextWindow = textinput.New()
	m.modelContextWindow.Prompt = "context length · "
	m.modelContextWindow.Placeholder = "provider metadata"
	m.modelContextWindow.CharLimit = 12
	m.modelContextWindow.SetWidth(28)
	m.modelGroupNameInput = textinput.New()
	m.modelGroupNameInput.Prompt = "group name · "
	m.modelGroupNameInput.Placeholder = "heavy"
	m.modelGroupNameInput.CharLimit = 64
	m.modelGroupNameInput.SetWidth(40)
	m.modelGroupTimeoutInput = textinput.New()
	m.modelGroupTimeoutInput.Prompt = "timeout · "
	m.modelGroupTimeoutInput.Placeholder = "none (for example 60s or 2m)"
	m.modelGroupTimeoutInput.CharLimit = 32
	m.modelGroupTimeoutInput.SetWidth(48)
	m.gatewaySettingsStore = gatewayconfig.Store{Dir: store.Dir}
	m.gatewayHostInput = textinput.New()
	m.gatewayHostInput.Prompt = "host · "
	m.gatewayHostInput.SetWidth(40)
	m.gatewayPortInput = textinput.New()
	m.gatewayPortInput.Prompt = "port · "
	m.gatewayPortInput.CharLimit = 5
	m.gatewayPortInput.SetWidth(16)
	m.gatewayKeyAlias = textinput.New()
	m.gatewayKeyAlias.Prompt = "alias · "
	m.gatewayKeyAlias.CharLimit = 64
	m.gatewayKeyAlias.SetWidth(48)
	m.librarySettingsStore = qlibrary.ConfigStore{Dir: store.Dir}
	m.mcpSettingsStore = mcpconfig.Store{Dir: store.Dir}
	m.libraryHostInput = textinput.New()
	m.libraryHostInput.Prompt = "host · "
	m.libraryHostInput.SetWidth(40)
	m.libraryPortInput = textinput.New()
	m.libraryPortInput.Prompt = "port · "
	m.libraryPortInput.CharLimit = 5
	m.libraryPortInput.SetWidth(16)
	loomPrompts := []string{"artifact MiB · ", "store MiB · ", "GC trigger % · ", "GC target % · ", "grace hours · "}
	for index := range m.loomInputs {
		field := textinput.New()
		field.Prompt = loomPrompts[index]
		field.CharLimit = 8
		field.SetWidth(28)
		m.loomInputs[index] = field
	}
	m.input = newChatInput()
	m.ignoreEditor = newIgnoreEditor()
	m.skillsInput = textinput.New()
	m.skillsInput.Prompt = "git · "
	m.skillsInput.Placeholder = "https://github.com/owner/skill.git"
	m.skillsInput.SetWidth(72)
	m.skillsInput.CharLimit = 2048
	lspPlaceholders := []string{"profile ID or project path", "languages or language", "command or server override", `arguments as JSON, e.g. ["serve"]`}
	for index := range m.lspInputs {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = lspPlaceholders[index]
		field.SetWidth(72)
		field.CharLimit = 4096
		m.lspInputs[index] = field
	}
	mcpPlaceholders := []string{"server ID", "stdio or streamable-http", "command or https://… URL", `arguments as JSON, e.g. ["serve"]`, `child env mapping, e.g. {"TOKEN":"TOKEN"}`, `header env mapping, e.g. {"Authorization":"MCP_AUTH"}`}
	for index := range m.mcpInputs {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = mcpPlaceholders[index]
		field.SetWidth(72)
		field.CharLimit = 4096
		m.mcpInputs[index] = field
	}
	agentsPlaceholders := []string{"connection ID", "codex or grok", "custom ACP command", `arguments as JSON, e.g. ["agent","stdio"]`, `environment as JSON, e.g. {"TOKEN":"value"}`, "authentication method ID"}
	for index := range m.agentsInputs {
		field := textinput.New()
		field.Prompt = ""
		field.Placeholder = agentsPlaceholders[index]
		field.SetWidth(72)
		field.CharLimit = 4096
		m.agentsInputs[index] = field
	}
	if runtime != nil && len(m.gatewayConfig.Providers) == 0 {
		m.enterProviderEditor(-1)
	}
	return m
}

func newChatInput() textarea.Model {
	input := textarea.New()
	// A real terminal cursor gives Windows IMEs an accurate composition and
	// candidate-window position. The default virtual cursor is only painted text.
	input.SetVirtualCursor(false)
	styles := input.Styles()
	styles.Cursor.Shape = tea.CursorBar
	input.SetStyles(styles)
	input.Placeholder = "Type a message…"
	input.Prompt = "│ "
	input.ShowLineNumbers = false
	input.SetHeight(4)
	input.SetWidth(80)
	input.CharLimit = 32_000
	input.KeyMap.InsertNewline.SetKeys("shift+enter")
	input.KeyMap.InsertNewline.SetHelp("shift+enter", "insert newline")
	return input
}

func newIgnoreEditor() textarea.Model {
	editor := textarea.New()
	editor.Placeholder = "# One discovery pattern per line\n.gradle/\nbuild/\nvendor/"
	editor.Prompt = ""
	editor.ShowLineNumbers = true
	editor.SetWidth(80)
	editor.SetHeight(12)
	editor.CharLimit = 1 << 20
	editor.KeyMap.InsertNewline.SetKeys("enter")
	editor.KeyMap.InsertNewline.SetHelp("enter", "new line")
	return editor
}

func (m model) Init() tea.Cmd {
	if m.initializing && m.startup != nil {
		return tea.Batch(m.startup, m.spinner.Tick, tea.RequestBackgroundColor)
	}
	if m.screen == screenChat {
		return tea.Batch(m.input.Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenModels {
		return tea.Batch(m.modelPickerFocus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenProviders {
		return tea.RequestBackgroundColor
	}
	if m.screen == screenGatewayNetwork {
		return tea.Batch(m.gatewayNetworkFocusCommand(), tea.RequestBackgroundColor)
	}
	if m.screen == screenGatewayKeys && m.gatewayKeyAdding {
		return tea.Batch(m.gatewayKeyAlias.Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenGateway || m.screen == screenGatewayKeys {
		return tea.RequestBackgroundColor
	}
	if m.screen == screenLibrary {
		return tea.Batch(m.libraryNetworkFocusCommand(), tea.RequestBackgroundColor)
	}
	if m.screen == screenLoom {
		return tea.Batch(m.loomFocusCommand(), tea.RequestBackgroundColor)
	}
	if m.screen == screenIgnore {
		return tea.Batch(m.ignoreEditor.Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenLSP && m.lspMode != lspModeList {
		return tea.Batch(m.lspInputs[m.lspFormFocus].Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenMCP && m.mcpMode != mcpModeList {
		return tea.Batch(m.mcpInputs[m.mcpFormFocus].Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenAgents && m.agentsMode != agentsModeList {
		return tea.Batch(m.agentsInputs[m.agentsFormFocus].Focus(), tea.RequestBackgroundColor)
	}
	if m.screen == screenHelp {
		return tea.RequestBackgroundColor
	}
	if m.screen == screenSessions {
		return tea.RequestBackgroundColor
	}
	return tea.Batch(m.setup[m.setupFocus].Focus(), tea.RequestBackgroundColor)
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	message = normalizeTextInputMessage(message)
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.resize(message.Width, message.Height)
		return m, nil
	case tea.BackgroundColorMsg:
		m.applyColorScheme(message.IsDark())
		return m, nil
	case acpSessionResetMsg:
		if message.err != nil {
			m.status = "Start new ACP session: " + message.err.Error()
			return m, m.input.Focus()
		}
		m.resetConversation()
		m.memory = memory.New(memoryPolicy(m.activeConfig()), m.messages)
		m.status = "New ACP session · " + string(message.sessionID)
		m.resize(m.width, m.height)
		return m, m.input.Focus()
	case commitFinishedMsg:
		m.commitRunning = false
		if message.err != nil {
			m.status = "Commit failed · " + message.err.Error()
		} else {
			m.status = commitResultStatus(message.result)
		}
		m.resize(m.width, m.height)
		return m, m.input.Focus()
	case runtimeInitializedMsg:
		m.initializing = false
		m.startup = nil
		m.toolRuntime = message.tools
		m.archive = message.archive
		m.archiveSearch = message.archiveSearch
		m.archiveErr = message.archiveErr
		m.libraryClient = message.library
		m.models = append([]client.Model(nil), message.models...)
		m.gatewayConfig = message.gatewayConfig
		waitingForSession := m.sessionPickerRequired
		if waitingForSession {
			m.config = message.config
			m.client = message.client
		} else if message.client != nil {
			m.enterChat(message.config, message.client)
		} else if len(message.gatewayConfig.Providers) > 0 {
			m.enterProviderList()
		} else {
			m.enterSetup(message.config)
		}
		var statuses []string
		if message.err != nil {
			statuses = append(statuses, message.err.Error())
		}
		if message.startupErr != nil {
			statuses = append(statuses, message.startupErr.Error())
		}
		if message.archiveErr != nil {
			statuses = append(statuses, "archive: "+message.archiveErr.Error())
		}
		if message.mcpErr != nil {
			statuses = append(statuses, "MCP: "+message.mcpErr.Error())
		}
		for _, status := range message.mcpStatuses {
			if status.Error != "" {
				statuses = append(statuses, "MCP "+status.ID+": "+status.Error)
			}
		}
		if len(statuses) > 0 {
			m.status = strings.Join(statuses, " · ")
		}
		m.resize(m.width, m.height)
		if m.screen == screenChat {
			return m, tea.Batch(m.input.Focus(), m.startNextLearningSegment())
		}
		if m.screen == screenSessions {
			return m, nil
		}
		if m.screen == screenHelp {
			return m, nil
		}
		if m.screen == screenProviders {
			return m, nil
		}
		return m, m.setup[m.setupFocus].Focus()
	case thinkerResultMsg:
		if message.jobID == "" || message.jobID != m.thinkerJobID || message.sessionGeneration != m.sessionGeneration {
			return m, nil
		}
		m.thinkerBusy = false
		m.thinkerJobID = ""
		if message.err != nil {
			m.archiveFailure("thinker", message.err)
			_ = m.flushArchive()
			if !m.waiting {
				m.status = "Thinker: " + message.err.Error()
				m.resize(m.width, m.height)
			}
			return m, nil
		}
		if m.learning != nil {
			if err := m.learning.Commit(message.jobID); err != nil {
				m.status = "Thinker checkpoint: " + err.Error()
				return m, nil
			}
			if err := m.saveWorkspaceSession(); err != nil {
				m.status = err.Error()
				return m, nil
			}
		}
		if !m.waiting && message.result.Processed > 0 {
			m.status = fmt.Sprintf(
				"Thinker processed %d proposition(s) · %d created · %d merged · %d discarded",
				message.result.Processed, message.result.Created, message.result.Merged, message.result.Discarded,
			)
			m.resize(m.width, m.height)
		}
		return m, m.startNextLearningSegment()
	case configuredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			if m.screen == screenModels {
				return m, m.modelFilter.Focus()
			}
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.client != nil && m.client != message.client {
			_ = m.client.Close()
		}
		embeddingCommand := m.configureEmbeddingRuntime(message.config, message.client)
		if m.isStandaloneScreen(screenModels) {
			m.config = message.config
			m.draftConfig = message.config
			m.client = message.client
			m.screen = screenModels
			m.modelPickerStage = modelPickerTargets
			m.modelFilter.Blur()
			m.status = "Model changed to " + message.config.Provider.Model
			return m, embeddingCommand
		}
		if message.preserveHistory {
			m.screen = screenChat
			m.setupEdit = false
			m.config = message.config
			m.client = message.client
			m.conversationID = ""
			m.compactionTarget = 0
			if m.memory != nil {
				m.memory.Configure(memoryPolicy(m.activeConfig()))
			}
			if override, found := m.workspaceOverride(defaultModelTarget); found {
				m.status = "Default model changed to " + message.config.Provider.Model + " · workspace model remains " + override.Model
			} else {
				m.status = "Model changed to " + message.config.Provider.Model
			}
			m.refreshTranscript()
			m.refreshLearningContextLength()
		} else {
			m.enterChat(message.config, message.client)
		}
		return m, tea.Batch(m.input.Focus(), m.startNextLearningSegment(), embeddingCommand)
	case archiveEmbeddingConfiguredMsg:
		if message.err != nil {
			m.status = "Embedding: " + message.err.Error()
		} else if message.stats.Embedded > 0 {
			m.status = fmt.Sprintf("Embedded %d workspace history record(s)", message.stats.Embedded)
		}
		return m, nil
	case modelTargetConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelPickerFocus()
		}
		m.config = message.config
		m.draftConfig = message.config
		m.refreshModelGroupCatalog()
		m.screen = screenModels
		m.modelPickerStage = modelPickerTargets
		m.status = message.target + " model settings saved"
		m.modelFilter.Blur()
		m.embeddingDimensions.Blur()
		return m, nil
	case workspaceModelConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelPickerFocus()
		}
		m.workspaceModel = message.config
		m.conversationID = ""
		m.compactionTarget = 0
		if m.memory != nil {
			m.memory.Configure(memoryPolicy(m.activeConfig()))
		}
		m.screen = screenModels
		m.modelPickerStage = modelPickerTargets
		m.modelFilter.Blur()
		if message.cleared {
			m.status = "Workspace " + message.target + " model cleared · inheriting active/global setting"
		} else {
			m.status = "Workspace " + message.target + " model changed to " + message.config.Overrides[message.target].Model
		}
		m.refreshTranscript()
		return m, nil
	case mcpSettingsSavedMsg:
		m.mcpBusy = false
		if message.err != nil {
			m.status = message.err.Error()
			return m, nil
		}
		m.mcpDraft = cloneMCPConfig(message.config)
		m.mcpOriginal = cloneMCPConfig(message.config)
		m.mcpDiscardArmed = false
		m.status = renderMCPSaveStatus(message.statuses, m.toolRuntime == nil)
		return m, nil
	case agentsSettingsSavedMsg:
		m.agentsBusy = false
		if message.err != nil {
			m.status = message.err.Error()
			return m, nil
		}
		m.config = message.config
		m.agentsDraft = cloneConfigForAgents(message.config)
		m.agentsOriginal = cloneConfigForAgents(message.config)
		m.agentsDiscardArmed = false
		m.status = "Agent settings saved"
		return m, nil
	case agentConnectionProbedMsg:
		m.agentsBusy = false
		if m.agentsProbe == nil {
			m.agentsProbe = make(map[string]string)
		}
		if message.err != nil {
			m.agentsProbe[message.id] = "failed"
			m.status = message.id + " connection failed · " + message.err.Error()
			return m, nil
		}
		m.agentsProbe[message.id] = "connected"
		m.status = message.id + " connected · initialize/session lifecycle passed"
		return m, nil
	case modelGroupsConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelPickerFocus()
		}
		m.config = message.config
		m.draftConfig = message.config
		m.refreshModelGroupCatalog()
		m.modelPickerStage = modelPickerGroups
		m.modelGroupDeleteArmed = false
		m.status = "Model groups saved"
		m.selectModelGroup(message.group)
		return m, nil
	case modelsResultMsg:
		m.discovering = false
		if message.err != nil {
			m.status = message.err.Error()
			if m.modelReturn == screenChat {
				m.screen = screenChat
				return m, m.input.Focus()
			}
			m.screen = screenSetup
			return m, m.setup[m.setupFocus].Focus()
		}
		m.enterModelPicker(message.config, message.models)
		if m.screen == screenSetup {
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.modelPickerFocus()
	case providersAppliedMsg:
		m.discovering = false
		if message.err != nil {
			m.status = message.err.Error()
			if m.screen == screenProviders {
				return m, nil
			}
			if m.screen == screenModels {
				return m, m.modelPickerFocus()
			}
			return m, m.setup[m.setupFocus].Focus()
		}
		if m.gatewayConfigOnly {
			m.gatewayConfig = message.gatewayConfig
			m.enterProviderList()
			m.status = "Provider settings saved"
			return m, nil
		}
		if message.replaceClient {
			if m.client != nil && m.client != message.client {
				_ = m.client.Close()
			}
			m.client = message.client
		}
		m.gatewayConfig = message.gatewayConfig
		if strings.TrimSpace(m.config.Provider.Model) == "" || m.config.Provider.Model == "model-discovery" {
			m.config = message.config
		} else {
			m.config.Provider.ContextWindow = message.config.Provider.ContextWindow
			if m.memory != nil {
				m.memory.Configure(memoryPolicy(m.activeConfig()))
			}
		}
		m.conversationID = ""
		m.compactionTarget = 0
		if message.openModels {
			m.enterModelPicker(message.config, message.models)
			return m, m.modelPickerFocus()
		}
		if message.modelTarget != "" {
			m.screen = screenModels
			m.modelPickerStage = modelPickerModels
			m.draftConfig = message.config
			m.modelContextWindow.Blur()
			m.status = "Gateway model metadata saved"
			if message.warning != "" {
				m.status += " · " + message.warning
			}
			return m, m.modelFilter.Focus()
		}
		m.enterProviderList()
		m.status = "Provider settings saved"
		if message.warning != "" {
			m.status += " · " + message.warning
		}
		return m, nil
	case lspDiscoveryMsg:
		return m.applyLSPDiscovery(message)
	case agentEventMsg:
		if message.turnID != 0 && message.turnID != m.turnID {
			return m, nil
		}
		event := message.event
		if event.taskStarted != nil {
			m.activeTask = cloneActiveTask(event.taskStarted)
			if err := m.saveWorkspaceSession(); err != nil {
				m.status = err.Error()
			}
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.taskCompleted {
			m.activeTask = nil
			if err := m.saveWorkspaceSession(); err != nil {
				m.status = err.Error()
			}
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.streamDelta != nil {
			delta := *event.streamDelta
			switch delta.Kind {
			case chatStreamThinking:
				if delta.Start || len(m.transcriptThoughts) == 0 {
					m.transcriptThoughts = append(m.transcriptThoughts, transcriptThought{Before: len(m.messages)})
				}
				m.transcriptThoughts[len(m.transcriptThoughts)-1].Content += delta.Content
				m.status = "Thinking…"
			case chatStreamResponse:
				if delta.Start {
					m.streamResponse = ""
				}
				m.streamResponse += delta.Content
				m.status = "Responding…"
			}
			m.refreshTranscript()
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.learningName != "" {
			command := m.enqueueLearningSpecial(event.learningName, event.learningPayload)
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID), command)
		}
		if event.plan != nil {
			m.status = agentPlanStatus(*event.plan)
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.activity != nil {
			m.appendAgentActivity(*event.activity)
			m.status = activityStatus(*event.activity)
			m.resize(m.width, m.height)
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.trace != nil {
			m.appendAgentTrace(*event.trace)
			m.resize(m.width, m.height)
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.status != "" {
			m.status = event.status
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID))
		}
		if event.call != nil {
			m.archiveToolCall(*event.call)
			m.status = "Running tool · " + describeToolCall(*event.call)
			var learning tea.Cmd
			if event.call.Function.Name == "learn" {
				learning = m.enqueueExplicitLearning()
			}
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID), learning)
		}
		if event.question != nil {
			m.asking = true
			m.pendingQuestion = *event.question
			m.questionChoice = 0
			m.questionAnswer = event.answer
			m.questionEvents = message.events
			m.questionTurnID = message.turnID
			m.input.Reset()
			m.input.Placeholder = "Type a custom answer…"
			m.status = "Choose an option or type a custom answer"
			m.resize(m.width, m.height)
			if len(m.pendingQuestion.Choices) > 0 {
				m.questionViewport.GotoBottom()
			} else {
				m.questionViewport.GotoTop()
			}
			return m, m.input.Focus()
		}
		if event.message != nil {
			m.streamResponse = ""
			m.messages = append(m.messages, *event.message)
			learning := m.observeLearningMessage(*event.message)
			if m.memory != nil {
				m.memory.Append(*event.message)
			}
			if event.message.Role == client.RoleAssistant {
				m.archiveMessage(*event.message, sessionstore.StatusSucceeded, false)
				m.status = "Preparing tool call…"
			} else {
				status := sessionstore.StatusSucceeded
				if event.toolIsError {
					status = sessionstore.StatusFailed
				}
				m.archiveMessage(*event.message, status, event.toolIsError)
				m.status = "Thinking… · " + event.message.Name + " completed"
			}
			m.refreshTranscript()
			return m, tea.Batch(m.spinner.Tick, waitAgentEvent(message.events, message.turnID), learning)
		}
		return m.Update(chatResultMsg{
			turnID:   message.turnID,
			response: event.response, requestEstimate: event.requestEstimate,
			toolCalls: event.toolCalls, err: event.err,
		})
	case chatResultMsg:
		if message.turnID != 0 && message.turnID != m.turnID {
			return m, nil
		}
		m.finishTurn()
		m.waiting = false
		m.compacting = false
		m.asking = false
		m.pendingQuestion = askToUserInput{}
		m.questionAnswer = nil
		m.questionEvents = nil
		m.questionTurnID = 0
		m.pendingMessage = client.Message{}
		m.streamResponse = ""
		if message.err != nil {
			m.compactionTarget = 0
			m.archiveFailure("chat", message.err)
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status = message.err.Error() + " · archive: " + archiveErr.Error()
				m.offerPlanExecutionResume()
				return m, m.input.Focus()
			}
			m.status = message.err.Error()
			m.offerPlanExecutionResume()
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.compactionTarget = 0
			err := errors.New("provider returned no choices")
			m.archiveFailure("chat", err)
			m.status = "provider returned no choices"
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			m.offerPlanExecutionResume()
			return m, m.input.Focus()
		}
		assistant := message.response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		if message.response.ConversationID != "" {
			m.conversationID = message.response.ConversationID
		}
		m.messages = append(m.messages, message.intermediate...)
		m.messages = append(m.messages, assistant)
		learningCommands := make([]tea.Cmd, 0, len(message.intermediate)+2)
		for _, intermediate := range message.intermediate {
			learningCommands = append(learningCommands, m.observeLearningMessage(intermediate))
		}
		learningCommands = append(learningCommands, m.observeLearningMessage(assistant))
		for _, intermediate := range message.intermediate {
			m.archiveMessage(intermediate, sessionstore.StatusSucceeded, false)
		}
		m.archiveMessage(assistant, sessionstore.StatusSucceeded, false)
		if m.memory != nil {
			for _, intermediate := range message.intermediate {
				m.memory.Append(intermediate)
			}
			requestEstimate := message.requestEstimate
			if requestEstimate == 0 {
				requestEstimate = m.requestEstimate
			}
			m.memory.ObserveUsage(message.response.Usage.PromptTokens, requestEstimate)
			m.memory.Append(assistant)
		}
		if m.compactionTarget > 0 && message.response.Usage.PromptTokens > m.compactionTarget {
			m.status = fmt.Sprintf("Context remains above target · %s/%s", formatTokens(message.response.Usage.PromptTokens), formatTokens(m.compactionTarget))
		} else {
			m.status = ""
			if message.toolCalls > 0 {
				m.status = fmt.Sprintf("Tools used · %d", message.toolCalls)
			}
		}
		if len(m.agentTraces) > 0 {
			m.agentTraceExpanded = false
			if m.status == "" {
				m.status = "Plan completed · ctrl+g inspect subagent trace"
			}
		}
		if cacheStatus := promptCacheStatus(message.response.Usage); cacheStatus != "" {
			if m.status != "" {
				m.status += " · "
			}
			m.status += cacheStatus
		}
		m.compactionTarget = 0
		m.sessionUpdatedAt = time.Now().UTC()
		m.resize(m.width, m.height)
		if err := m.saveWorkspaceSession(); err != nil {
			m.status = err.Error()
		}
		if err := m.flushArchive(); err != nil {
			m.status = "archive: " + err.Error()
		}
		focus := m.input.Focus()
		learningCommands = append(learningCommands, focus, m.startNextLearningSegment())
		return m, tea.Batch(learningCommands...)
	case compactionResultMsg:
		if message.turnID != 0 && message.turnID != m.turnID {
			return m, nil
		}
		if message.err != nil {
			m.rollbackPendingMessage()
			m.archiveFailure("context_compaction", message.err)
			m.status = "compact context: " + message.err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		if message.response == nil || len(message.response.Choices) == 0 {
			m.rollbackPendingMessage()
			err := errors.New("provider returned no choices")
			m.archiveFailure("context_compaction", err)
			m.status = "compact context: provider returned no choices"
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		summary := strings.TrimSpace(message.response.Choices[0].Message.TextContent())
		if err := m.memory.Apply(message.plan, summary); err != nil {
			m.rollbackPendingMessage()
			m.archiveFailure("context_compaction", err)
			m.status = "compact context: " + err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		m.conversationID = ""
		m.archiveSummary(summary)
		m.compacting = false
		m.compactionTarget = message.plan.TargetTokens
		m.status = fmt.Sprintf("Context compacted · %s → %s", formatTokens(message.plan.BeforeTokens), formatTokens(m.memory.PredictedTokens()))
		return m, m.sendChatRequest()
	case deferredSubmitMsg:
		if !m.submitPending {
			return m, nil
		}
		m.submitPending = false
		return m.submitChat()
	case loomStatsMsg:
		m.loomBusy = false
		if message.err != nil {
			m.status = message.err.Error()
		} else {
			m.loomStats = message.stats
			m.status = ""
		}
		return m, m.loomFocusCommand()
	case loomActionMsg:
		m.loomBusy = false
		exitAfterSave := m.loomExitAfterSave
		m.loomExitAfterSave = false
		if message.config.Provider.Model != "" {
			m.config = message.config
			m.loomDraft = message.config.EffectiveLoom()
		}
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.loomFocusCommand()
		}
		m.loomStats = message.stats
		m.status = "Loom settings saved"
		if message.result != nil {
			prefix := "GC preview"
			if !message.result.DryRun {
				prefix = "GC completed"
			}
			m.status = fmt.Sprintf("%s · %d artifacts · %s reclaimed", prefix, message.result.ArtifactsRemoved, formatBytes(message.result.BytesReclaimed))
		}
		if exitAfterSave {
			m.screen = screenChat
			m.blurLoomInputs()
			return m, m.input.Focus()
		}
		return m, m.loomFocusCommand()
	case skillActionMsg:
		m.skillsBusy = false
		m.skillsStatusError = message.err != nil
		if message.err != nil {
			m.status = message.err.Error()
		} else {
			m.status = message.detail
		}
		m.refreshSkills(false)
		return m, nil
	case gatewaySettingsSavedMsg:
		if message.err != nil {
			m.status = message.err.Error()
			command := m.gatewayNetworkFocusCommand()
			return m, command
		}
		m.gatewaySettings = message.config
		m.status = "Gateway network settings saved; restart a running standalone Gateway to rebind"
		command := m.gatewayNetworkFocusCommand()
		return m, command
	case gatewayAPIKeyGeneratedMsg:
		if message.err != nil {
			m.status = message.err.Error()
			m.gatewayKeyAdding = true
			return m, m.gatewayKeyAlias.Focus()
		}
		m.gatewaySettings = message.config
		m.gatewayKeyAdding = false
		m.gatewayKeyAlias.Reset()
		m.gatewayKeyAlias.Blur()
		m.generatedGatewayKey = message.key
		m.gatewayKeyCursor = len(message.config.APIKeys) - 1
		m.status = ""
		return m, nil
	case gatewayAPIKeyRevokedMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, nil
		}
		m.gatewaySettings = message.config
		m.status = "API key revoked"
		return m, nil
	case librarySettingsSavedMsg:
		if message.err != nil {
			m.status = message.err.Error()
			command := m.libraryNetworkFocusCommand()
			return m, command
		}
		m.librarySettings = message.config
		m.status = "Library network settings saved; restart the running Library leader to rebind"
		command := m.libraryNetworkFocusCommand()
		return m, command
	}

	if key, ok := message.(tea.KeyPressMsg); ok {
		if key.String() == "ctrl+c" {
			if m.screen == screenChat && m.waiting {
				return m.interruptTurn()
			}
			return m, tea.Quit
		}
		if key.String() == "ctrl+h" {
			if m.screen == screenHelp {
				if m.isStandaloneScreen(screenHelp) {
					return m, tea.Quit
				}
				return m.leaveHelp()
			}
			return m.enterHelp()
		}
		if m.screen == screenSetup {
			return m.updateSetup(key)
		}
		if m.screen == screenProviders {
			return m.updateProviders(key)
		}
		if m.screen == screenGateway {
			return m.updateGateway(key)
		}
		if m.screen == screenGatewayNetwork {
			return m.updateGatewayNetwork(key)
		}
		if m.screen == screenGatewayKeys {
			return m.updateGatewayKeys(key)
		}
		if m.screen == screenLibrary {
			return m.updateLibrary(key)
		}
		if m.screen == screenModels {
			return m.updateModelPicker(key)
		}
		if m.screen == screenLoom {
			return m.updateLoom(key)
		}
		if m.screen == screenIgnore {
			return m.updateIgnore(key)
		}
		if m.screen == screenSkills {
			return m.updateSkills(key)
		}
		if m.screen == screenLSP {
			return m.updateLSP(key)
		}
		if m.screen == screenMCP {
			return m.updateMCP(key)
		}
		if m.screen == screenAgents {
			return m.updateAgents(key)
		}
		if m.screen == screenHelp {
			return m.updateHelp(key)
		}
		if m.screen == screenSessions {
			return m.updateSessions(key)
		}
		return m.updateChatKey(key)
	}

	if m.screen == screenIgnore {
		before := m.ignoreEditor.Value()
		var command tea.Cmd
		m.ignoreEditor, command = m.ignoreEditor.Update(message)
		if m.ignoreEditor.Value() != before {
			m.ignoreDiscardArmed = false
			m.status = ""
		}
		return m, command
	}
	if m.screen == screenHelp {
		var command tea.Cmd
		m.helpViewport, command = m.helpViewport.Update(message)
		return m, command
	}
	if m.screen == screenSkills {
		if m.skillsMode == skillModeAdd {
			var command tea.Cmd
			m.skillsInput, command = m.skillsInput.Update(message)
			return m, command
		}
		return m, nil
	}
	if m.screen == screenLSP && m.lspMode != lspModeList {
		var command tea.Cmd
		m.lspInputs[m.lspFormFocus], command = m.lspInputs[m.lspFormFocus].Update(message)
		return m, command
	}
	if m.screen == screenMCP && m.mcpMode != mcpModeList {
		var command tea.Cmd
		m.mcpInputs[m.mcpFormFocus], command = m.mcpInputs[m.mcpFormFocus].Update(message)
		return m, command
	}
	if m.screen == screenAgents && m.agentsMode != agentsModeList {
		var command tea.Cmd
		m.agentsInputs[m.agentsFormFocus], command = m.agentsInputs[m.agentsFormFocus].Update(message)
		return m, command
	}
	if m.screen == screenGatewayNetwork || (m.screen == screenGatewayKeys && m.gatewayKeyAdding) {
		return m.updateGatewayInput(message)
	}
	if m.screen == screenLibrary {
		return m.updateLibraryInput(message)
	}

	if m.screen == screenChat {
		var commands []tea.Cmd
		var command tea.Cmd
		if m.asking {
			m.questionViewport, command = m.questionViewport.Update(message)
		} else if m.waiting && m.agentTraceExpanded && len(m.agentTraces) > 0 {
			m.agentTraceViewport, command = m.agentTraceViewport.Update(message)
		} else {
			m.viewport, command = m.viewport.Update(message)
		}
		commands = append(commands, command)
		if (m.waiting || m.initializing) && !m.asking {
			m.spinner, command = m.spinner.Update(message)
			commands = append(commands, command)
		} else {
			m.input, command = m.input.Update(message)
			m.syncSlashCompletion()
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)
	}
	return m, nil
}

func (m model) updateSetup(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.discovering {
		return m, nil
	}
	switch key.String() {
	case "esc":
		if m.runtime != nil {
			if len(m.gatewayConfig.Providers) > 0 {
				m.enterProviderList()
				return m, nil
			}
			return m, tea.Quit
		}
		if m.setupEdit && m.client != nil {
			m.screen = screenChat
			m.status = ""
			return m, m.input.Focus()
		}
		return m, tea.Quit
	case "tab", "down", "enter":
		if key.String() == "enter" && m.setupFocus == setupFieldCount-1 {
			if m.runtime != nil {
				return m.applyProviderEdit()
			}
			return m.discoverModels()
		}
		return m.moveSetupFocus(1)
	case "shift+tab", "up":
		return m.moveSetupFocus(-1)
	}
	if m.runtime != nil && m.setupFocus == setupProviderType {
		switch key.String() {
		case "left":
			m.cycleProviderType(-1)
		case "right", " ":
			m.cycleProviderType(1)
		}
		return m, nil
	}
	if m.runtime != nil && m.setupFocus == setupProviderKind {
		switch key.String() {
		case "left":
			m.cycleProviderKind(-1)
		case "right", " ":
			m.cycleProviderKind(1)
		}
		return m, nil
	}
	var command tea.Cmd
	m.setup[m.setupFocus], command = m.setup[m.setupFocus].Update(key)
	return m, command
}

func (m model) updateProviders(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	providers := m.gatewayConfig.Providers
	if m.discovering {
		return m, nil
	}
	switch key.String() {
	case "esc":
		if m.providerReturn == screenGateway {
			m.enterGatewaySettings()
			return m, nil
		}
		if m.gatewayConfigOnly {
			return m, tea.Quit
		}
		if m.client != nil && m.config.Provider.Model != "model-discovery" {
			m.screen = screenChat
			m.status = ""
			return m, m.input.Focus()
		}
		return m, tea.Quit
	case "up":
		if m.providerCursor > 0 {
			m.providerCursor--
		}
		return m, nil
	case "down":
		if m.providerCursor < len(providers)-1 {
			m.providerCursor++
		}
		return m, nil
	case "a":
		m.enterProviderEditor(-1)
		return m, m.setup[m.setupFocus].Focus()
	case "enter":
		if len(providers) == 0 {
			m.enterProviderEditor(-1)
		} else {
			m.enterProviderEditor(m.providerCursor)
		}
		return m, m.setup[m.setupFocus].Focus()
	case " ":
		if len(providers) == 0 {
			return m, nil
		}
		candidate := cloneGatewayConfig(m.gatewayConfig)
		candidate.Providers[m.providerCursor].Enabled = !candidate.Providers[m.providerCursor].Enabled
		if enabledProviderCount(candidate) == 0 {
			m.status = "At least one provider must remain enabled"
			return m, nil
		}
		return m.applyGatewayConfig(candidate)
	case "d":
		if len(providers) <= 1 {
			m.status = "Add another provider before deleting this one"
			return m, nil
		}
		candidate := cloneGatewayConfig(m.gatewayConfig)
		candidate.Providers = append(candidate.Providers[:m.providerCursor], candidate.Providers[m.providerCursor+1:]...)
		if enabledProviderCount(candidate) == 0 {
			m.status = "At least one provider must remain enabled"
			return m, nil
		}
		if m.providerCursor >= len(candidate.Providers) {
			m.providerCursor = len(candidate.Providers) - 1
		}
		return m.applyGatewayConfig(candidate)
	}
	return m, nil
}

func (m model) applyProviderEdit() (tea.Model, tea.Cmd) {
	id := strings.TrimSpace(m.setup[setupProviderID].Value())
	prefix := strings.TrimSpace(m.setup[setupProviderPrefix].Value())
	providerType := m.selectedProviderType().value
	providerKind := m.selectedProviderKind().value
	if id == "" {
		m.status = "Provider ID is required"
		return m, nil
	}
	if strings.Contains(id, "/") {
		m.status = "Provider ID must not contain '/'"
		return m, nil
	}
	if strings.Contains(prefix, "/") {
		m.status = "Provider prefix must not contain '/'"
		return m, nil
	}
	if providerType == "" {
		m.status = "Provider type is required"
		return m, nil
	}

	candidate := cloneGatewayConfig(m.gatewayConfig)
	editing := !m.providerAdding && m.providerEditIndex >= 0 && m.providerEditIndex < len(candidate.Providers)
	effectivePrefix := prefix
	if effectivePrefix == "" {
		effectivePrefix = id
	}
	for index, existing := range candidate.Providers {
		if editing && index == m.providerEditIndex {
			continue
		}
		if existing.ID == id {
			m.status = "Provider ID " + id + " is already in use"
			return m, nil
		}
		existingPrefix := existing.Prefix
		if existingPrefix == "" {
			existingPrefix = existing.ID
		}
		if existingPrefix == effectivePrefix {
			m.status = "Provider prefix " + effectivePrefix + " is already in use"
			return m, nil
		}
	}
	provider := gateway.ProviderConfig{Enabled: true}
	if editing {
		provider = candidate.Providers[m.providerEditIndex]
	}
	provider.ID = id
	provider.Prefix = prefix
	provider.Type = providerType
	provider.Kind = providerKind
	provider.BaseURL = strings.TrimSpace(m.setup[setupBaseURL].Value())
	provider.APIKeyEnv = strings.TrimSpace(m.setup[setupAPIKeyEnv].Value())
	provider.APIKey = m.setup[setupAPIKey].Value()
	if editing {
		candidate.Providers[m.providerEditIndex] = provider
	} else {
		candidate.Providers = append(candidate.Providers, provider)
	}
	return m.applyGatewayConfig(candidate)
}

func (m model) applyGatewayConfig(candidate gateway.Config) (tea.Model, tea.Cmd) {
	if m.runtime == nil {
		m.status = "internal Gateway runtime is unavailable"
		return m, nil
	}
	if m.gatewayConfigOnly {
		m.discovering = true
		m.status = "Saving Gateway settings…"
		for index := range m.setup {
			m.setup[index].Blur()
		}
		runtime := m.runtime
		return m, func() tea.Msg {
			if err := runtime.Apply(m.ctx, candidate); err != nil {
				return providersAppliedMsg{err: err}
			}
			return providersAppliedMsg{gatewayConfig: candidate}
		}
	}
	m.discovering = true
	returnTarget := ""
	if m.screen == screenModels && m.modelPickerStage == modelPickerContextWindow {
		returnTarget = m.modelTarget
	} else {
		m.modelChooseTarget = false
	}
	m.status = "Saving Gateway settings…"
	for index := range m.setup {
		m.setup[index].Blur()
	}
	m.input.Blur()
	if m.client != nil && m.config.Provider.Model != "model-discovery" {
		m.modelReturn = screenChat
	} else {
		m.modelReturn = screenSetup
	}
	value := m.config
	needsModelDiscovery := strings.TrimSpace(value.Provider.Model) == "" || value.Provider.Model == "model-discovery"
	if strings.TrimSpace(value.Provider.Model) == "" {
		value = config.Default()
		value.Provider.Model = "model-discovery"
	}
	value.UseManagedGateway()
	if returnTarget != "" && m.modelSelection.ID == value.Provider.Model {
		if providerIndex, upstreamID, found := gatewayModelLocation(candidate, m.modelSelection.ID); found {
			value.Provider.ContextWindow = candidate.Providers[providerIndex].ModelMetadata[upstreamID].ContextLength
		}
	}
	runtime := m.runtime
	factory := m.factory
	return m, func() tea.Msg {
		if err := runtime.Apply(m.ctx, candidate); err != nil {
			return providersAppliedMsg{err: err}
		}
		result := providersAppliedMsg{
			config: value, gatewayConfig: candidate, modelTarget: returnTarget, replaceClient: true,
		}
		if returnTarget != "" {
			if err := m.store.Save(value); err != nil {
				result.warning = "save active context cache: " + err.Error()
			}
		}
		configuredClient, err := factory(value)
		if err != nil {
			result.warning = appendStatusWarning(result.warning, "refresh Gateway client: "+err.Error())
			return result
		}
		result.client = configuredClient
		if !needsModelDiscovery {
			return result
		}
		models, err := configuredClient.ListModels(m.ctx)
		if err != nil {
			result.warning = appendStatusWarning(result.warning, "model discovery unavailable: "+err.Error())
			return result
		}
		if len(models) == 0 {
			result.warning = appendStatusWarning(result.warning, "Gateway returned no models")
			return result
		}
		if refreshed, found := refreshModelContextWindow(value, models); found {
			value = refreshed
			if err := m.store.Save(value); err != nil {
				result.warning = appendStatusWarning(result.warning, "save model context: "+err.Error())
			}
		}
		result.models = models
		result.config = value
		result.openModels = true
		return result
	}
}

func appendStatusWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + " · " + next
}

func enabledProviderCount(value gateway.Config) int {
	count := 0
	for _, provider := range value.Providers {
		if provider.Enabled {
			count++
		}
	}
	return count
}

func cloneGatewayConfig(value gateway.Config) gateway.Config {
	result := value
	result.Providers = append([]gateway.ProviderConfig(nil), value.Providers...)
	for index := range result.Providers {
		source := value.Providers[index].ModelMetadata
		if source == nil {
			continue
		}
		result.Providers[index].ModelMetadata = make(map[string]client.ModelMetadata, len(source))
		for modelID, metadata := range source {
			result.Providers[index].ModelMetadata[modelID] = metadata
		}
	}
	return result
}

func containsModel(models []client.Model, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}
func refreshModelContextWindow(value config.Config, models []client.Model) (config.Config, bool) {
	for _, candidate := range models {
		if candidate.ID == value.Provider.Model {
			value.Provider.ContextWindow = candidate.ContextLength
			return value, true
		}
	}
	return value, false
}

func gatewayModelLocation(value gateway.Config, modelID string) (int, string, bool) {
	prefix, upstreamID, found := strings.Cut(modelID, "/")
	if !found || prefix == "" || upstreamID == "" {
		return 0, "", false
	}
	for index, provider := range value.Providers {
		effectivePrefix := provider.Prefix
		if effectivePrefix == "" {
			effectivePrefix = provider.ID
		}
		if effectivePrefix == prefix {
			return index, upstreamID, true
		}
	}
	return 0, "", false
}

// streamsActiveChat limits token streaming to providers that use the shared
// OpenAI-compatible HTTP transport or the native Codex stream. Native
// Anthropic retains its existing one-shot chat behavior.
func (m model) streamsActiveChat() bool {
	return modelSupportsChatStreaming(m.gatewayConfig, m.activeConfig().ModelGroups, m.activeModel(), nil)
}

func modelSupportsChatStreaming(
	gatewayConfig gateway.Config,
	groups map[string]config.ModelGroupConfig,
	modelID string,
	seen map[string]bool,
) bool {
	if name, grouped := strings.CutPrefix(modelID, "group/"); grouped {
		if seen == nil {
			seen = make(map[string]bool)
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		defer delete(seen, name)
		group, found := groups[name]
		if !found || len(group.Candidates) == 0 {
			return false
		}
		for _, candidate := range group.Candidates {
			if !modelSupportsChatStreaming(gatewayConfig, groups, candidate.Model, seen) {
				return false
			}
		}
		return true
	}
	providerIndex, _, found := gatewayModelLocation(gatewayConfig, modelID)
	if !found {
		return false
	}
	switch gatewayConfig.Providers[providerIndex].Type {
	case "openai-compatible", "openrouter", "xai", "grok", "codex", "codex-app-server":
		return true
	default:
		return false
	}
}

// modelNeedsSystemInstructionCoalescing identifies OpenAI-compatible routes
// whose downstream chat templates may treat developer messages as additional
// system messages and reject them after the first message.
func modelNeedsSystemInstructionCoalescing(
	gatewayConfig gateway.Config,
	groups map[string]config.ModelGroupConfig,
	modelID string,
	seen map[string]bool,
) bool {
	if name, grouped := strings.CutPrefix(modelID, "group/"); grouped {
		if seen == nil {
			seen = make(map[string]bool)
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		defer delete(seen, name)
		group, found := groups[name]
		if !found {
			return false
		}
		for _, candidate := range group.Candidates {
			if modelNeedsSystemInstructionCoalescing(gatewayConfig, groups, candidate.Model, seen) {
				return true
			}
		}
		return false
	}
	providerIndex, _, found := gatewayModelLocation(gatewayConfig, modelID)
	return found && gatewayConfig.Providers[providerIndex].Type == "openai-compatible"
}

func (m model) gatewayContextWindowOverride(modelID string) (int64, bool) {
	providerIndex, upstreamID, found := gatewayModelLocation(m.gatewayConfig, modelID)
	if !found {
		return 0, false
	}
	metadata, configured := m.gatewayConfig.Providers[providerIndex].ModelMetadata[upstreamID]
	return metadata.ContextLength, configured && metadata.ContextLength > 0
}

func (m model) selectedProviderType() providerTypeOption {
	if m.providerTypeCursor < 0 || m.providerTypeCursor >= len(providerTypeOptions) {
		return providerTypeOptions[0]
	}
	return providerTypeOptions[m.providerTypeCursor]
}

func providerKindOptions(providerType string) []providerKindOption {
	automatic := providerKindOption{
		label:       "Auto (inferred)",
		description: "Infer OpenAI semantics, except api.x.ai endpoints are inferred as Grok.",
	}
	if providerType != "openai-compatible" {
		kind := nativeProviderKind(providerType)
		automatic.description = "Infer " + providerKindLabel(kind) + " semantics from this native API type."
		return []providerKindOption{
			automatic,
			{value: kind, label: providerKindLabel(kind), description: "Explicit matching kind for this native API type."},
		}
	}
	return []providerKindOption{
		automatic,
		{value: "generic", label: "Generic", description: "Strict compatible API; disable provider-specific prompt-cache extensions."},
		{value: "openai", label: "OpenAI", description: "Use OpenAI prompt_cache_key semantics."},
		{value: "openrouter", label: "OpenRouter", description: "Use OpenRouter session_id sticky routing semantics."},
		{value: "grok", label: "Grok / xAI", description: "Use Grok conversation-affinity headers and xAI capabilities."},
		{value: "anthropic", label: "Claude / Anthropic", description: "Use Anthropic automatic prompt-cache semantics."},
		{value: "codex", label: "Codex App Server", description: "Use Codex provider-managed conversation semantics."},
	}
}

func nativeProviderKind(providerType string) string {
	switch providerType {
	case "openrouter":
		return "openrouter"
	case "xai", "grok":
		return "grok"
	case "anthropic", "claude":
		return "anthropic"
	case "codex", "codex-app-server":
		return "codex"
	default:
		return "openai"
	}
}

func providerKindLabel(kind string) string {
	switch kind {
	case "openrouter":
		return "OpenRouter"
	case "grok":
		return "Grok / xAI"
	case "anthropic":
		return "Claude / Anthropic"
	case "codex":
		return "Codex App Server"
	default:
		return "OpenAI"
	}
}

func canonicalProviderKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return ""
	case "openai", "openai-compatible":
		return "openai"
	case "openrouter":
		return "openrouter"
	case "grok", "xai":
		return "grok"
	case "anthropic", "claude":
		return "anthropic"
	case "codex", "codex-app-server":
		return "codex"
	case "generic":
		return "generic"
	default:
		return ""
	}
}

func (m model) selectedProviderKind() providerKindOption {
	options := providerKindOptions(m.selectedProviderType().value)
	if m.providerKindCursor < 0 || m.providerKindCursor >= len(options) {
		return options[0]
	}
	return options[m.providerKindCursor]
}

func (m *model) setProviderKind(value string) {
	value = canonicalProviderKind(value)
	options := providerKindOptions(m.selectedProviderType().value)
	for index, option := range options {
		if option.value == value {
			m.providerKindCursor = index
			return
		}
	}
	// Changing to a native transport must not retain an incompatible semantic
	// kind from the previous OpenAI-compatible route. Empty means infer the
	// matching native kind and always passes the type/kind validation matrix.
	m.providerKindCursor = 0
}

func (m *model) cycleProviderKind(delta int) {
	options := providerKindOptions(m.selectedProviderType().value)
	m.providerKindCursor = (m.providerKindCursor + delta + len(options)) % len(options)
}

func (m *model) setProviderType(value string) {
	switch value {
	case "grok":
		value = "xai"
	case "claude":
		value = "anthropic"
	case "codex-app-server":
		value = "codex"
	}
	for index, option := range providerTypeOptions {
		if option.value == value {
			m.providerTypeCursor = index
			m.updateProviderTypePlaceholders()
			return
		}
	}
	m.providerTypeCursor = 0
	m.updateProviderTypePlaceholders()
}

func (m *model) cycleProviderType(delta int) {
	previous := m.selectedProviderType()
	previousKind := m.selectedProviderKind().value
	baseURLWasDefault := m.setup[setupBaseURL].Value() == previous.baseURL
	apiKeyEnvWasDefault := m.setup[setupAPIKeyEnv].Value() == previous.apiKeyEnv
	m.providerTypeCursor = (m.providerTypeCursor + delta + len(providerTypeOptions)) % len(providerTypeOptions)
	next := m.selectedProviderType()
	m.updateProviderTypePlaceholders()
	m.setProviderKind(previousKind)
	if baseURLWasDefault {
		m.setup[setupBaseURL].SetValue(next.baseURL)
	}
	if apiKeyEnvWasDefault {
		m.setup[setupAPIKeyEnv].SetValue(next.apiKeyEnv)
	}
}

func (m *model) updateProviderTypePlaceholders() {
	selected := m.selectedProviderType()
	m.setup[setupBaseURL].Placeholder = selected.baseURLHint
	m.setup[setupAPIKeyEnv].Placeholder = selected.apiKeyHint
}

func (m model) moveSetupFocus(delta int) (tea.Model, tea.Cmd) {
	m.setup[m.setupFocus].Blur()
	m.setupFocus = (m.setupFocus + delta + setupFieldCount) % setupFieldCount
	if m.runtime != nil && (m.setupFocus == setupProviderType || m.setupFocus == setupProviderKind) {
		return m, nil
	}
	return m, m.setup[m.setupFocus].Focus()
}

func (m model) discoverModels() (tea.Model, tea.Cmd) {
	value := config.Default()
	if m.setupEdit {
		value = m.config
	}
	value.Provider.BaseURL = strings.TrimSpace(m.setup[setupBaseURL].Value())
	// Config validation requires a model, but discovery intentionally happens
	// before the user chooses one.
	value.Provider.Model = "model-discovery"
	value.Provider.APIKeyEnv = strings.TrimSpace(m.setup[setupAPIKeyEnv].Value())
	value.Provider.APIKey = m.setup[setupAPIKey].Value()
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Loading models…"
	m.discovering = true
	m.modelChooseTarget = false
	m.modelReturn = screenSetup
	for index := range m.setup {
		m.setup[index].Blur()
	}
	return m, func() tea.Msg {
		discoveryClient, err := m.factory(value)
		if err != nil {
			return modelsResultMsg{err: err}
		}
		defer discoveryClient.Close()
		models, err := discoveryClient.ListModels(m.ctx)
		if err != nil {
			return modelsResultMsg{err: fmt.Errorf("load models: %w", err)}
		}
		return modelsResultMsg{models: models, config: value}
	}
}

func (m model) discoverCurrentModels() (tea.Model, tea.Cmd) {
	if m.client == nil {
		m.status = "provider is not configured"
		return m, nil
	}
	m.screen = screenModels
	m.modelReturn = screenChat
	m.modelChooseTarget = true
	m.draftConfig = m.config
	m.models = nil
	m.modelCursor = 0
	m.modelFilter.Reset()
	m.modelFilter.Blur()
	m.input.Blur()
	m.discovering = true
	m.status = "Loading models…"
	configuredClient := m.client
	return m, func() tea.Msg {
		models, err := configuredClient.ListModels(m.ctx)
		if err != nil {
			return modelsResultMsg{err: fmt.Errorf("load models: %w", err)}
		}
		return modelsResultMsg{models: models, config: m.config}
	}
}

func (m model) updateModelPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.discovering {
		return m, nil
	}
	switch m.modelPickerStage {
	case modelPickerTargets:
		return m.updateModelTargetPicker(key)
	case modelPickerGroups:
		return m.updateModelGroups(key)
	case modelPickerGroupName:
		return m.updateModelGroupName(key)
	case modelPickerGroupCandidates:
		return m.updateModelGroupCandidates(key)
	case modelPickerGroupCandidateModels:
		return m.updateModelGroupCandidateModels(key)
	case modelPickerGroupCandidateReasoning:
		return m.updateModelGroupCandidateReasoning(key)
	case modelPickerGroupCandidateTimeout:
		return m.updateModelGroupCandidateTimeout(key)
	case modelPickerReasoning:
		return m.updateReasoningPicker(key)
	case modelPickerEmbeddingDimensions:
		return m.updateEmbeddingDimensionsPicker(key)
	case modelPickerContextWindow:
		return m.updateContextWindowPicker(key)
	}
	filtered := m.filteredModels()
	switch key.String() {
	case "esc":
		if m.modelChooseTarget {
			m.modelPickerStage = modelPickerTargets
			m.modelFilter.Blur()
			m.status = ""
			return m, nil
		}
		if m.runtime != nil && m.modelReturn == screenChat && !containsModel(filtered, m.activeConfig().Provider.Model) {
			m.status = "Select a model from the updated providers"
			return m, nil
		}
		m.screen = m.modelReturn
		m.status = ""
		m.modelFilter.Blur()
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.setup[m.setupFocus].Focus()
	case "up":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
		return m, nil
	case "down":
		if m.modelCursor < len(filtered)-1 {
			m.modelCursor++
		}
		return m, nil
	case "pgup":
		m.modelCursor = max(0, m.modelCursor-10)
		return m, nil
	case "pgdown":
		m.modelCursor = min(max(0, len(filtered)-1), m.modelCursor+10)
		return m, nil
	case "ctrl+e":
		if len(filtered) == 0 {
			return m, nil
		}
		if _, grouped := modelGroupChoice(m.draftConfig, filtered[m.modelCursor].ID); grouped {
			m.status = "Context overrides apply to models, not model groups"
			return m, nil
		}
		return m.enterContextWindowPicker(filtered[m.modelCursor])
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		value := m.draftConfig
		selected := filtered[m.modelCursor]
		if m.modelWorkspace {
			return m.saveWorkspaceModel(m.modelTarget, selected)
		}
		if m.modelTarget == "" || m.modelTarget == defaultModelTarget {
			value.Provider.Model = selected.ID
			value.Provider.ContextWindow = selected.ContextLength
			return m.saveConfiguration(value, m.modelReturn == screenChat)
		}
		if m.modelTarget == embeddingModelTarget {
			value.Embedding.Model = selected.ID
			m.draftConfig = value
			m.modelSelection = selected
			m.modelPickerStage = modelPickerEmbeddingDimensions
			m.embeddingDimensions.SetValue("")
			if value.Embedding.Dimensions > 0 {
				m.embeddingDimensions.SetValue(strconv.Itoa(value.Embedding.Dimensions))
			}
			m.modelFilter.Blur()
			return m, m.embeddingDimensions.Focus()
		}
		if group, grouped := modelGroupChoice(value, selected.ID); grouped {
			value = withAgentGroup(value, m.modelTarget, group)
			if _, err := subagent.Resolve(value, m.modelTarget, m.models); err != nil {
				m.status = err.Error()
				return m, nil
			}
			return m.saveModelTargetConfiguration(value, m.modelTarget)
		}
		value = withAgentModel(value, m.modelTarget, selected.ID)
		m.draftConfig = value
		m.modelSelection = selected
		reasoning := selectedReasoning(selected)
		if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
			value = withAgentReasoningEffort(value, m.modelTarget, "")
			return m.saveModelTargetConfiguration(value, m.modelTarget)
		}
		m.modelPickerStage = modelPickerReasoning
		m.reasoningCursor = reasoningEffortCursor(value, m.modelTarget, reasoning.SupportedEfforts)
		m.modelFilter.Blur()
		return m, nil
	}
	var command tea.Cmd
	m.modelFilter, command = m.modelFilter.Update(key)
	m.modelCursor = 0
	return m, command
}

func (m model) updateModelTargetPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	targets := m.modelTargets()
	switch key.String() {
	case "esc":
		if m.isStandaloneScreen(screenModels) {
			return m, tea.Quit
		}
		m.screen = m.modelReturn
		m.status = ""
		if m.screen == screenChat {
			return m, m.input.Focus()
		}
		return m, m.setup[m.setupFocus].Focus()
	case "up":
		if m.modelTargetCursor > 0 {
			m.modelTargetCursor--
		}
	case "down":
		if m.modelTargetCursor < len(targets)-1 {
			m.modelTargetCursor++
		}
	case "left":
		m.modelScopeCursor = modelScopeGlobal
	case "right", "tab":
		m.modelScopeCursor = modelScopeWorkspace
	case "g":
		m.modelPickerStage = modelPickerGroups
		m.modelGroupDeleteArmed = false
		m.status = ""
		m.selectModelGroup("")
		return m, nil
	case "enter":
		if len(targets) == 0 {
			return m, nil
		}
		m.modelTarget = targets[m.modelTargetCursor]
		m.modelWorkspace = m.modelScopeCursor == modelScopeWorkspace
		if m.modelWorkspace && !workspace.ModelOverrideAllowed(m.modelTarget) {
			m.status = modelTargetLabel(m.modelTarget) + " is shared globally and cannot be overridden per workspace"
			return m, nil
		}
		m.modelPickerStage = modelPickerModels
		m.modelFilter.Reset()
		m.selectConfiguredModel()
		return m, m.modelFilter.Focus()
	case "i":
		if len(targets) == 0 {
			return m, nil
		}
		target := targets[m.modelTargetCursor]
		if m.modelScopeCursor == modelScopeWorkspace {
			if !workspace.ModelOverrideAllowed(target) {
				m.status = modelTargetLabel(target) + " has no workspace override"
				return m, nil
			}
			return m.clearWorkspaceModel(target)
		}
		if target == defaultModelTarget {
			m.status = "The GLOBAL default model is required and cannot be reset"
			return m, nil
		}
		value := m.draftConfig
		if target == embeddingModelTarget {
			value.Embedding = config.EmbeddingConfig{}
		} else {
			value = withoutAgentOverride(value, target)
		}
		return m.saveModelTargetConfiguration(value, target)
	}
	return m, nil
}

func (m model) updateModelGroups(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	names := modelGroupNames(m.draftConfig)
	if len(names) == 0 {
		m.modelGroupCursor = 0
	} else {
		m.modelGroupCursor = min(m.modelGroupCursor, len(names)-1)
	}
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerTargets
		m.status = ""
		m.modelGroupDeleteArmed = false
		return m, nil
	case "up":
		if m.modelGroupCursor > 0 {
			m.modelGroupCursor--
		}
		m.modelGroupDeleteArmed = false
	case "down":
		if m.modelGroupCursor < len(names)-1 {
			m.modelGroupCursor++
		}
		m.modelGroupDeleteArmed = false
	case "a":
		m.modelGroupName = ""
		m.modelGroupDraft = config.ModelGroupConfig{}
		m.modelGroupNameInput.SetValue("")
		m.modelPickerStage = modelPickerGroupName
		m.status = ""
		return m, m.modelGroupNameInput.Focus()
	case "enter":
		if len(names) == 0 {
			return m, nil
		}
		m.beginModelGroupEdit(names[m.modelGroupCursor])
		return m, nil
	case "d":
		if len(names) == 0 {
			return m, nil
		}
		name := names[m.modelGroupCursor]
		roles := rolesUsingModelGroup(m.draftConfig, name)
		for role, override := range m.workspaceModel.Overrides {
			if override.Model == "group/"+name {
				roles = append(roles, "workspace "+role)
			}
		}
		if len(roles) > 0 {
			m.status = "Group is used by: " + strings.Join(roles, ", ")
			m.modelGroupDeleteArmed = false
			return m, nil
		}
		if !m.modelGroupDeleteArmed {
			m.modelGroupDeleteArmed = true
			m.status = "Press d again to delete group " + name
			return m, nil
		}
		value := m.draftConfig
		value.ModelGroups = cloneModelGroups(value.ModelGroups)
		delete(value.ModelGroups, name)
		return m.saveModelGroupsConfiguration(value, "")
	}
	return m, nil
}

func (m model) updateModelGroupName(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelGroupNameInput.Blur()
		m.modelPickerStage = modelPickerGroups
		m.status = ""
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.modelGroupNameInput.Value())
		if name == "" || name != m.modelGroupNameInput.Value() {
			m.status = "Group name must be non-empty without surrounding whitespace"
			return m, nil
		}
		if _, exists := m.draftConfig.ModelGroups[name]; exists {
			m.status = "Model group already exists: " + name
			return m, nil
		}
		m.modelGroupName = name
		m.modelGroupDraft = config.ModelGroupConfig{}
		m.modelGroupCandidateCursor = 0
		m.modelGroupNameInput.Blur()
		m.modelPickerStage = modelPickerGroupCandidates
		m.status = "Add at least one candidate"
		return m, nil
	}
	var command tea.Cmd
	m.modelGroupNameInput, command = m.modelGroupNameInput.Update(key)
	return m, command
}

func (m model) updateModelGroupCandidates(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	candidates := m.modelGroupDraft.Candidates
	if len(candidates) == 0 {
		m.modelGroupCandidateCursor = 0
	} else {
		m.modelGroupCandidateCursor = min(m.modelGroupCandidateCursor, len(candidates)-1)
	}
	switch key.String() {
	case "esc":
		if len(m.modelGroupDraft.Candidates) == 0 {
			if _, existing := m.draftConfig.ModelGroups[m.modelGroupName]; existing {
				m.status = "A model group requires at least one candidate"
				return m, nil
			}
			m.modelPickerStage = modelPickerGroups
			m.status = "Empty group discarded"
			return m, nil
		}
		return m.saveCurrentModelGroup()
	case "up":
		if m.modelGroupCandidateCursor > 0 {
			m.modelGroupCandidateCursor--
		}
	case "down":
		if m.modelGroupCandidateCursor < len(candidates)-1 {
			m.modelGroupCandidateCursor++
		}
	case "ctrl+up":
		index := m.modelGroupCandidateCursor
		if index > 0 {
			m.modelGroupDraft.Candidates[index-1], m.modelGroupDraft.Candidates[index] =
				m.modelGroupDraft.Candidates[index], m.modelGroupDraft.Candidates[index-1]
			m.modelGroupCandidateCursor--
		}
	case "ctrl+down":
		index := m.modelGroupCandidateCursor
		if index >= 0 && index < len(candidates)-1 {
			m.modelGroupDraft.Candidates[index+1], m.modelGroupDraft.Candidates[index] =
				m.modelGroupDraft.Candidates[index], m.modelGroupDraft.Candidates[index+1]
			m.modelGroupCandidateCursor++
		}
	case "a":
		return m.beginModelGroupCandidateEdit(-1)
	case "enter":
		if len(candidates) == 0 {
			return m, nil
		}
		return m.beginModelGroupCandidateEdit(m.modelGroupCandidateCursor)
	case "d":
		if len(candidates) == 0 {
			return m, nil
		}
		if len(candidates) == 1 {
			m.status = "A model group requires at least one candidate; delete the group from the previous screen instead"
			return m, nil
		}
		index := m.modelGroupCandidateCursor
		m.modelGroupDraft.Candidates = append(m.modelGroupDraft.Candidates[:index], m.modelGroupDraft.Candidates[index+1:]...)
		m.modelGroupCandidateCursor = min(index, len(m.modelGroupDraft.Candidates)-1)
		m.modelGroupCandidateCursor = max(0, m.modelGroupCandidateCursor)
	}
	return m, nil
}

func (m model) saveCurrentModelGroup() (tea.Model, tea.Cmd) {
	if err := subagent.ValidateModelGroup(m.modelGroupName, m.modelGroupDraft, m.models); err != nil {
		m.status = err.Error()
		return m, nil
	}
	value := m.draftConfig
	value.ModelGroups = cloneModelGroups(value.ModelGroups)
	value.ModelGroups[m.modelGroupName] = cloneModelGroup(m.modelGroupDraft)
	return m.saveModelGroupsConfiguration(value, m.modelGroupName)
}

func (m model) beginModelGroupCandidateEdit(index int) (tea.Model, tea.Cmd) {
	m.modelGroupCandidateEdit = index
	m.modelGroupCandidate = config.ModelCandidateConfig{}
	if index >= 0 && index < len(m.modelGroupDraft.Candidates) {
		m.modelGroupCandidate = m.modelGroupDraft.Candidates[index]
	}
	m.modelPickerStage = modelPickerGroupCandidateModels
	m.modelFilter.Reset()
	m.modelCursor = 0
	for candidateIndex, model := range m.candidateModels() {
		if model.ID == m.modelGroupCandidate.Model {
			m.modelCursor = candidateIndex
			break
		}
	}
	m.status = ""
	return m, m.modelFilter.Focus()
}

func (m model) updateModelGroupCandidateModels(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	filtered := m.filteredCandidateModels()
	switch key.String() {
	case "esc":
		m.modelFilter.Blur()
		m.modelPickerStage = modelPickerGroupCandidates
		return m, nil
	case "up":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
		return m, nil
	case "down":
		if m.modelCursor < len(filtered)-1 {
			m.modelCursor++
		}
		return m, nil
	case "enter":
		if len(filtered) == 0 {
			return m, nil
		}
		m.modelSelection = filtered[m.modelCursor]
		m.modelGroupCandidate.Model = m.modelSelection.ID
		reasoning := selectedReasoning(m.modelSelection)
		m.modelFilter.Blur()
		if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
			m.modelGroupCandidate.ReasoningEffort = ""
			return m.enterModelGroupCandidateTimeout()
		}
		m.modelPickerStage = modelPickerGroupCandidateReasoning
		m.reasoningCursor = effortCursor(m.modelGroupCandidate.ReasoningEffort, reasoning.SupportedEfforts)
		return m, nil
	}
	var command tea.Cmd
	m.modelFilter, command = m.modelFilter.Update(key)
	m.modelCursor = 0
	return m, command
}

func (m model) updateModelGroupCandidateReasoning(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	reasoning := selectedReasoning(m.modelSelection)
	if reasoning == nil {
		m.modelPickerStage = modelPickerGroupCandidateModels
		return m, m.modelFilter.Focus()
	}
	options := append([]string{""}, reasoning.SupportedEfforts...)
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerGroupCandidateModels
		return m, m.modelFilter.Focus()
	case "up":
		if m.reasoningCursor > 0 {
			m.reasoningCursor--
		}
	case "down":
		if m.reasoningCursor < len(options)-1 {
			m.reasoningCursor++
		}
	case "enter":
		m.modelGroupCandidate.ReasoningEffort = options[m.reasoningCursor]
		return m.enterModelGroupCandidateTimeout()
	}
	return m, nil
}

func (m model) enterModelGroupCandidateTimeout() (tea.Model, tea.Cmd) {
	m.modelPickerStage = modelPickerGroupCandidateTimeout
	m.modelGroupTimeoutInput.SetValue("")
	if m.modelGroupCandidate.Timeout > 0 {
		m.modelGroupTimeoutInput.SetValue(m.modelGroupCandidate.Timeout.String())
	}
	return m, m.modelGroupTimeoutInput.Focus()
}

func (m model) updateModelGroupCandidateTimeout(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelGroupTimeoutInput.Blur()
		if selectedReasoning(m.modelSelection) != nil {
			m.modelPickerStage = modelPickerGroupCandidateReasoning
			return m, nil
		}
		m.modelPickerStage = modelPickerGroupCandidateModels
		return m, m.modelFilter.Focus()
	case "enter":
		raw := strings.TrimSpace(m.modelGroupTimeoutInput.Value())
		var timeout time.Duration
		if raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil || parsed <= 0 {
				m.status = "Timeout must be a positive duration such as 60s, or empty"
				return m, nil
			}
			timeout = parsed
		}
		m.modelGroupCandidate.Timeout = timeout
		for index, candidate := range m.modelGroupDraft.Candidates {
			if candidate.Model == m.modelGroupCandidate.Model && index != m.modelGroupCandidateEdit {
				m.status = "Model group already contains " + candidate.Model
				return m, nil
			}
		}
		if m.modelGroupCandidateEdit >= 0 && m.modelGroupCandidateEdit < len(m.modelGroupDraft.Candidates) {
			m.modelGroupDraft.Candidates[m.modelGroupCandidateEdit] = m.modelGroupCandidate
			m.modelGroupCandidateCursor = m.modelGroupCandidateEdit
		} else {
			m.modelGroupDraft.Candidates = append(m.modelGroupDraft.Candidates, m.modelGroupCandidate)
			m.modelGroupCandidateCursor = len(m.modelGroupDraft.Candidates) - 1
		}
		m.modelGroupTimeoutInput.Blur()
		m.modelPickerStage = modelPickerGroupCandidates
		m.status = "Candidate updated · changes save when leaving the group"
		return m, nil
	}
	var command tea.Cmd
	m.modelGroupTimeoutInput, command = m.modelGroupTimeoutInput.Update(key)
	return m, command
}

func (m *model) beginModelGroupEdit(name string) {
	m.modelGroupName = name
	m.modelGroupDraft = cloneModelGroup(m.draftConfig.ModelGroups[name])
	m.modelGroupCandidateCursor = 0
	m.modelPickerStage = modelPickerGroupCandidates
	m.modelGroupDeleteArmed = false
	m.status = ""
}

func (m *model) selectModelGroup(name string) {
	names := modelGroupNames(m.draftConfig)
	m.modelGroupCursor = 0
	for index, candidate := range names {
		if candidate == name {
			m.modelGroupCursor = index
			break
		}
	}
}

func (m *model) refreshModelGroupCatalog() {
	base := m.candidateModels()
	m.models = append([]client.Model(nil), base...)
	for _, name := range modelGroupNames(m.draftConfig) {
		group := m.draftConfig.ModelGroups[name]
		if !modelGroupAvailable(group, base) {
			continue
		}
		contextLength, output := modelGroupLimits(group, base)
		m.models = append(m.models, client.Model{
			ID: "group/" + name, Object: "model", OwnedBy: "q-model-group",
			ContextLength: contextLength, MaxOutputTokens: output,
		})
	}
	sort.Slice(m.models, func(i, j int) bool { return m.models[i].ID < m.models[j].ID })
}

func (m model) saveModelGroupsConfiguration(value config.Config, group string) (tea.Model, tea.Cmd) {
	if name, grouped := strings.CutPrefix(value.Provider.Model, "group/"); grouped {
		if configured, found := value.ModelGroups[name]; found {
			value.Provider.ContextWindow, _ = modelGroupLimits(configured, m.models)
		}
	}
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving model groups…"
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return modelGroupsConfiguredMsg{err: err}
		}
		return modelGroupsConfiguredMsg{config: value, group: group}
	}
}

func modelGroupNames(value config.Config) []string {
	names := make([]string, 0, len(value.ModelGroups))
	for name := range value.ModelGroups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func rolesUsingModelGroup(value config.Config, group string) []string {
	roles := make([]string, 0)
	if value.Provider.Model == "group/"+group {
		roles = append(roles, "default")
	}
	for role, agent := range value.Agents.Roles {
		if agent.Group == group || agent.Model == "group/"+group {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return roles
}

func cloneModelGroups(source map[string]config.ModelGroupConfig) map[string]config.ModelGroupConfig {
	result := make(map[string]config.ModelGroupConfig, len(source)+1)
	for name, group := range source {
		result[name] = cloneModelGroup(group)
	}
	return result
}

func cloneModelGroup(group config.ModelGroupConfig) config.ModelGroupConfig {
	group.Candidates = append([]config.ModelCandidateConfig(nil), group.Candidates...)
	return group
}

func effortCursor(configured string, efforts []string) int {
	for index, effort := range efforts {
		if effort == configured {
			return index + 1
		}
	}
	return 0
}

func (m model) updateReasoningPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	reasoning := selectedReasoning(m.modelSelection)
	if reasoning == nil || len(reasoning.SupportedEfforts) == 0 {
		m.modelPickerStage = modelPickerModels
		return m, m.modelFilter.Focus()
	}
	options := append([]string{""}, reasoning.SupportedEfforts...)
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerModels
		return m, m.modelFilter.Focus()
	case "up":
		if m.reasoningCursor > 0 {
			m.reasoningCursor--
		}
	case "down":
		if m.reasoningCursor < len(options)-1 {
			m.reasoningCursor++
		}
	case "enter":
		value := withAgentReasoningEffort(m.draftConfig, m.modelTarget, options[m.reasoningCursor])
		return m.saveModelTargetConfiguration(value, m.modelTarget)
	}
	return m, nil
}

func (m model) updateEmbeddingDimensionsPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerModels
		m.embeddingDimensions.Blur()
		return m, m.modelFilter.Focus()
	case "enter":
		dimensions, err := strconv.Atoi(strings.TrimSpace(m.embeddingDimensions.Value()))
		if err != nil || dimensions < 1 || dimensions > 4096 {
			m.status = "Embedding dimensions must be a number between 1 and 4096"
			return m, nil
		}
		value := m.draftConfig
		value.Embedding.Dimensions = dimensions
		return m.saveModelTargetConfiguration(value, embeddingModelTarget)
	}
	var command tea.Cmd
	m.embeddingDimensions, command = m.embeddingDimensions.Update(key)
	return m, command
}
func (m model) enterContextWindowPicker(selected client.Model) (tea.Model, tea.Cmd) {
	if m.runtime == nil {
		m.status = "Gateway model metadata is unavailable for this provider"
		return m, nil
	}
	if _, _, found := gatewayModelLocation(m.gatewayConfig, selected.ID); !found {
		m.status = "Model is not mapped to a configured Gateway provider"
		return m, nil
	}
	m.modelSelection = selected
	m.modelPickerStage = modelPickerContextWindow
	m.modelContextWindow.SetValue("")
	if override, configured := m.gatewayContextWindowOverride(selected.ID); configured {
		m.modelContextWindow.SetValue(strconv.FormatInt(override, 10))
	}
	m.modelFilter.Blur()
	m.status = ""
	return m, m.modelContextWindow.Focus()
}

func (m model) updateContextWindowPicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.modelPickerStage = modelPickerModels
		m.modelContextWindow.Blur()
		m.status = ""
		return m, m.modelFilter.Focus()
	case "enter":
		raw := strings.TrimSpace(m.modelContextWindow.Value())
		var contextWindow int64
		if raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed <= 0 {
				m.status = "Context length must be a positive integer or empty"
				return m, nil
			}
			contextWindow = parsed
		}
		candidate := cloneGatewayConfig(m.gatewayConfig)
		providerIndex, upstreamID, found := gatewayModelLocation(candidate, m.modelSelection.ID)
		if !found {
			m.status = "Model is not mapped to a configured Gateway provider"
			return m, nil
		}
		provider := &candidate.Providers[providerIndex]
		if provider.ModelMetadata == nil {
			provider.ModelMetadata = make(map[string]client.ModelMetadata)
		}
		metadata := provider.ModelMetadata[upstreamID]
		metadata.ContextLength = contextWindow
		if metadata.ContextLength == 0 && metadata.MaxOutputTokens == 0 && metadata.Capabilities == nil {
			delete(provider.ModelMetadata, upstreamID)
		} else {
			provider.ModelMetadata[upstreamID] = metadata
		}
		if len(provider.ModelMetadata) == 0 {
			provider.ModelMetadata = nil
		}
		return m.applyGatewayConfig(candidate)
	}
	var command tea.Cmd
	m.modelContextWindow, command = m.modelContextWindow.Update(key)
	return m, command
}

func (m model) saveConfiguration(value config.Config, preserveHistory bool) (tea.Model, tea.Cmd) {
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving configuration…"
	m.modelFilter.Blur()
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return configuredMsg{err: err}
		}
		configuredClient, err := m.factory(value)
		return configuredMsg{config: value, client: configuredClient, preserveHistory: preserveHistory, err: err}
	}
}

func (m model) saveModelTargetConfiguration(value config.Config, target string) (tea.Model, tea.Cmd) {
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving " + target + " model settings…"
	m.modelFilter.Blur()
	m.embeddingDimensions.Blur()
	return m, func() tea.Msg {
		if err := m.store.Save(value); err != nil {
			return modelTargetConfiguredMsg{err: err}
		}
		return modelTargetConfiguredMsg{config: value, target: target}
	}
}

func (m model) updateChatKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.handleSlashCompletionKey(key) {
		return m, nil
	}
	if key.String() == "ctrl+o" {
		m.toggleToolResults()
		return m, nil
	}
	if key.String() == "ctrl+g" && len(m.agentTraces) > 0 {
		m.agentTraceExpanded = !m.agentTraceExpanded
		m.resize(m.width, m.height)
		return m, nil
	}
	if m.asking && len(m.pendingQuestion.Choices) > 0 && strings.TrimSpace(m.input.Value()) == "" {
		choiceCount := questionChoiceCount(m.pendingQuestion)
		switch key.String() {
		case "up", "shift+tab":
			m.questionChoice = (m.questionChoice - 1 + choiceCount) % choiceCount
			m.refreshQuestion()
			m.questionViewport.GotoBottom()
			return m, nil
		case "down", "tab":
			m.questionChoice = (m.questionChoice + 1) % choiceCount
			m.refreshQuestion()
			m.questionViewport.GotoBottom()
			return m, nil
		}
	}
	if m.asking && isQuestionScrollKey(key.String()) {
		var command tea.Cmd
		m.questionViewport, command = m.questionViewport.Update(key)
		return m, command
	}
	if !m.asking && m.agentTraceExpanded && len(m.agentTraces) > 0 && isAgentTraceScrollKey(key.String()) {
		var command tea.Cmd
		m.agentTraceViewport, command = m.agentTraceViewport.Update(key)
		return m, command
	}
	switch key.String() {
	case "esc":
		return m, tea.Quit
	case "ctrl+l":
		if !m.waiting && !m.planResumePending {
			if remote, ok := m.client.(*acpRemoteClient); ok {
				m.status = "Starting new ACP session…"
				return m, remote.resetSessionCommand(m.ctx)
			}
			m.resetConversation()
		}
		return m, nil
	case "ctrl+p":
		if !m.waiting {
			if _, ok := m.client.(*acpRemoteClient); ok {
				m.status = "Model and mode are controlled by the connected ACP agent"
				return m, nil
			}
			if m.runtime != nil {
				m.enterGatewaySettings()
				return m, nil
			}
			m.enterSetup(m.config)
			return m, m.setup[m.setupFocus].Focus()
		}
		return m, nil
	case "ctrl+s":
		return m.submitChat()
	case "enter":
		return m.deferChatSubmit()
	}
	var commands []tea.Cmd
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(key)
	commands = append(commands, command)
	if !m.waiting || m.asking {
		m.input, command = m.input.Update(key)
		m.syncSlashCompletion()
		commands = append(commands, command)
		if m.asking && !m.pendingQuestion.ChoiceOnly && len(m.pendingQuestion.Choices) > 0 && strings.TrimSpace(m.input.Value()) != "" &&
			!customAnswerSelected(m.pendingQuestion, m.questionChoice) {
			m.questionChoice = len(m.pendingQuestion.Choices)
			m.refreshQuestion()
			m.questionViewport.GotoBottom()
		}
	}
	return m, tea.Batch(commands...)
}

func (m model) deferChatSubmit() (tea.Model, tea.Cmd) {
	if (m.waiting && !m.asking) || m.submitPending || m.client == nil {
		return m, nil
	}
	m.submitPending = true
	return m, tea.Tick(imeCommitGracePeriod, func(time.Time) tea.Msg {
		return deferredSubmitMsg{}
	})
}

func (m model) submitChat() (tea.Model, tea.Cmd) {
	m.submitPending = false
	content := strings.TrimSpace(norm.NFC.String(m.input.Value()))
	if m.asking {
		return m.submitQuestionAnswer(content)
	}
	if m.waiting || content == "" || m.client == nil {
		return m, nil
	}
	_, remoteChat := m.client.(*acpRemoteClient)
	if remoteChat && content == "/new" {
		m.input.Reset()
		m.status = "Starting new ACP session…"
		return m, m.client.(*acpRemoteClient).resetSessionCommand(m.ctx)
	}
	if !remoteChat {
		if updated, command, handled := m.startSkillCommand(content); handled {
			return updated, command
		}
		if query, handled := parseAgentSearchCommand(content); handled {
			return m.startAgentSearch(query)
		}
		switch content {
		case "/plan":
			m.input.Reset()
			m.planArmed = true
			m.input.Placeholder = "Describe the work to plan…"
			m.status = "Plan mode · enter a planning request"
			m.resize(m.width, m.height)
			return m, m.input.Focus()
		case "/commit":
			return m.startCommit()
		case "/clear":
			m.input.Reset()
			m.resetConversation()
			return m, m.input.Focus()
		case "/new":
			m.input.Reset()
			if err := m.startNewWorkspaceSession(); err != nil {
				m.status = err.Error()
			}
			return m, m.input.Focus()
		case "/sessions":
			m.input.Reset()
			return m.enterSessions()
		case "/learn":
			m.input.Reset()
			if m.learningDisabled() {
				m.status = "Learning is disabled for this workspace · /learn on to enable"
				return m, m.input.Focus()
			}
			m.status = "Learning checkpoint enqueued"
			return m, tea.Batch(m.input.Focus(), m.enqueueExplicitLearning())
		case "/learn off":
			m.input.Reset()
			if err := m.setWorkspaceLearningDisabled(true); err != nil {
				m.status = err.Error()
				return m, m.input.Focus()
			}
			m.status = "Learning disabled for this workspace"
			return m, m.input.Focus()
		case "/learn on":
			m.input.Reset()
			if err := m.setWorkspaceLearningDisabled(false); err != nil {
				m.status = err.Error()
				return m, m.input.Focus()
			}
			m.status = "Learning enabled for this workspace"
			return m, tea.Batch(m.input.Focus(), m.startNextLearningSegment())
		case "/learn status":
			m.input.Reset()
			if m.learningDisabled() {
				m.status = "Learning is disabled for this workspace"
			} else {
				m.status = "Learning is enabled for this workspace"
			}
			return m, m.input.Focus()
		case "/model":
			m.input.Reset()
			return m.discoverCurrentModels()
		case "/gateway":
			m.input.Reset()
			if m.runtime != nil {
				m.enterGatewaySettings()
				return m, nil
			}
			m.enterSetup(m.config)
			return m, m.setup[m.setupFocus].Focus()
		case "/library":
			m.input.Reset()
			return m, m.enterLibrarySettings()
		case "/loom":
			m.input.Reset()
			return m.enterLoom()
		case "/ignore":
			m.input.Reset()
			return m.enterIgnore()
		case "/skills":
			m.input.Reset()
			return m.enterSkills()
		case "/lsp":
			m.input.Reset()
			return m.enterLSP()
		case "/mcp":
			m.input.Reset()
			return m.enterMCP()
		case "/agents":
			m.input.Reset()
			return m.enterAgents()
		case "/help":
			m.input.Reset()
			return m.enterHelp()
		}
		if strings.HasPrefix(content, "/plan ") {
			return m.startPlan(strings.TrimSpace(strings.TrimPrefix(content, "/plan")))
		}
		if m.planArmed {
			return m.startPlan(content)
		}
	}
	m.clearAgentActivities()
	m.resize(m.width, m.height)
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.touchSessionMetadata(content)
	userMessage := client.Message{Role: client.RoleUser, Content: content}
	m.archiveMessage(userMessage, sessionstore.StatusSubmitted, false)
	m.messages = append(m.messages, userMessage)
	learning := m.observeLearningMessage(userMessage)
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(m.activeConfig()), nil)
	}
	m.memory.Append(userMessage)
	m.pendingMessage = userMessage
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.refreshTranscript()
	if !remoteChat && m.memory.ShouldCompact() {
		plan, err := m.memory.Plan()
		if err != nil {
			m.rollbackPendingMessage()
			m.archiveFailure("context_compaction", err)
			m.status = err.Error()
			if archiveErr := m.flushArchive(); archiveErr != nil {
				m.status += " · archive: " + archiveErr.Error()
			}
			return m, m.input.Focus()
		}
		m.compacting = true
		m.status = "Compacting context…"
		return m, tea.Batch(m.spinner.Tick, m.compactContext(plan), learning)
	}
	m.status = "Thinking…"
	return m, tea.Batch(m.spinner.Tick, m.sendChatRequest(), learning)
}

func (m model) submitQuestionAnswer(content string) (tea.Model, tea.Cmd) {
	if m.planResumePending {
		return m.submitPlanResumeAnswer(content)
	}
	if m.questionAnswer == nil || m.questionEvents == nil {
		return m, nil
	}
	var answer askToUserOutput
	if content == "" {
		if len(m.pendingQuestion.Choices) == 0 {
			return m, nil
		}
		if customAnswerSelected(m.pendingQuestion, m.questionChoice) {
			m.status = "Type a custom answer below"
			m.input.Placeholder = "Type a custom answer…"
			return m, m.input.Focus()
		}
		choice := m.pendingQuestion.Choices[min(max(m.questionChoice, 0), len(m.pendingQuestion.Choices)-1)]
		answer = askToUserOutput{SelectedChoiceID: choice.ID}
	} else if customAnswerSelected(m.pendingQuestion, m.questionChoice) {
		answer = askToUserOutput{Freeform: content}
	} else {
		answer = answerForQuestion(m.pendingQuestion, content)
		if m.pendingQuestion.ChoiceOnly && answer.SelectedChoiceID == "" {
			m.status = "Choose one of the available permission options"
			m.input.Reset()
			return m, m.input.Focus()
		}
	}
	answerChannel := m.questionAnswer
	events := m.questionEvents
	turnID := m.questionTurnID
	turnContext := m.activeTurnContext()
	m.asking = false
	m.planArmed = false
	m.pendingQuestion = askToUserInput{}
	m.questionChoice = 0
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.input.Reset()
	m.input.Placeholder = "Type a message…"
	m.input.Blur()
	m.status = "Thinking…"
	m.resize(m.width, m.height)
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		select {
		case answerChannel <- answer:
			return waitAgentEvent(events, turnID)()
		case <-turnContext.Done():
			return agentEventMsg{events: events, event: agentEvent{err: turnContext.Err()}, turnID: turnID}
		}
	})
}

func (m *model) beginTurn() {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnID++
	m.streamResponse = ""
	m.turnContext, m.turnCancel = context.WithCancel(m.ctx)
}

func (m *model) finishTurn() {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnContext = nil
	m.turnCancel = nil
	m.turnMessageStart = 0
}

func (m model) activeTurnContext() context.Context {
	if m.turnContext != nil {
		return m.turnContext
	}
	return m.ctx
}

func (m model) interruptTurn() (tea.Model, tea.Cmd) {
	if !m.waiting {
		return m, nil
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnID++
	m.completeInterruptedToolCalls()
	m.archiveTurnCancelled("interrupted by user")
	m.turnContext = nil
	m.turnCancel = nil
	m.turnMessageStart = 0
	m.waiting = false
	m.compacting = false
	m.asking = false
	m.planArmed = false
	m.submitPending = false
	m.pendingMessage = client.Message{}
	m.streamResponse = ""
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.conversationID = ""
	m.compactionTarget = 0
	m.input.Reset()
	m.input.Placeholder = "Type a message…"
	m.status = "Turn interrupted"
	m.sessionUpdatedAt = time.Now().UTC()
	m.resize(m.width, m.height)
	if err := m.saveWorkspaceSession(); err != nil {
		m.status += " · " + err.Error()
	}
	if err := m.flushArchive(); err != nil {
		m.status += " · archive: " + err.Error()
	}
	m.offerPlanExecutionResume()
	return m, m.input.Focus()
}

func (m *model) completeInterruptedToolCalls() {
	start := min(max(m.turnMessageStart, 0), len(m.messages))
	completed := make(map[string]struct{})
	for _, message := range m.messages[start:] {
		if message.Role == client.RoleTool && message.ToolCallID != "" {
			completed[message.ToolCallID] = struct{}{}
		}
	}
	for _, message := range m.messages[start:] {
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				continue
			}
			if _, found := completed[call.ID]; found {
				continue
			}
			m.archiveToolCall(call)
			cancelled := client.Message{
				Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID,
				Content: "Tool error: interrupted by user",
			}
			m.messages = append(m.messages, cancelled)
			if m.memory != nil {
				m.memory.Append(cancelled)
			}
			m.archiveMessage(cancelled, sessionstore.StatusCancelled, true)
			completed[call.ID] = struct{}{}
		}
	}
	m.refreshTranscript()
}

func (m model) compactContext(plan memory.Plan) tea.Cmd {
	configuredClient := m.client
	modelID := m.activeModel()
	maxTokens := plan.OutputBudget
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	return func() tea.Msg {
		response, err := chatWithConversationRecovery(turnContext, configuredClient, client.ChatRequest{
			Model: modelID, Messages: plan.RequestMessages(), MaxCompletionTokens: &maxTokens,
		})
		return compactionResultMsg{turnID: turnID, response: response, plan: plan, err: err}
	}
}

func (m *model) sendChatRequest() tea.Cmd {
	history := m.memory.Messages()
	m.requestEstimate = memory.CountMessages(history)
	conversationID := m.conversationID
	configuredClient := m.client
	modelID := m.activeModel()
	workingDirectory := ""
	if m.workspaceStore != nil {
		workingDirectory = m.workspaceStore.Root
	}
	toolRuntime, toolRuntimeErr := configuredAgentToolRuntime(
		m.toolRuntime, mcpconfig.RoleDefault, m.activeConfig(), workingDirectory,
	)
	turnContext := m.activeTurnContext()
	turnID := m.turnID
	streamEnabled := m.streamsActiveChat()
	coalesceInstructions := modelNeedsSystemInstructionCoalescing(
		m.gatewayConfig, m.activeConfig().ModelGroups, modelID, nil,
	)
	activeTask := cloneActiveTask(m.activeTask)
	if toolRuntimeErr != nil {
		return func() tea.Msg {
			return chatResultMsg{turnID: turnID, requestEstimate: m.requestEstimate, err: toolRuntimeErr}
		}
	}
	if remote, ok := configuredClient.(*acpRemoteClient); ok {
		events := make(chan agentEvent)
		content := m.pendingMessage.TextContent()
		return func() tea.Msg {
			go remote.runPrompt(turnContext, content, events)
			return waitAgentEvent(events, turnID)()
		}
	}
	if toolRuntime == nil && !streamEnabled {
		return func() tea.Msg {
			response, err := chatWithConversationRecovery(turnContext, configuredClient, client.ChatRequest{
				Model: modelID, Messages: providerMessages(history, coalesceInstructions), ConversationID: conversationID,
			})
			return chatResultMsg{turnID: turnID, response: response, requestEstimate: m.requestEstimate, err: err}
		}
	}
	events := make(chan agentEvent)
	return func() tea.Msg {
		if toolRuntime == nil && streamEnabled {
			go streamSingleChat(turnContext, configuredClient, modelID, history, conversationID, coalesceInstructions, m.requestEstimate, events)
		} else {
			go streamAgentLoop(turnContext, configuredClient, toolRuntime, modelID, history, conversationID, activeTask, streamEnabled, coalesceInstructions, events)
		}
		return waitAgentEvent(events, turnID)()
	}
}

func streamSingleChat(
	ctx context.Context,
	configuredClient chatClient,
	modelID string,
	history []client.Message,
	conversationID string,
	coalesceInstructions bool,
	requestEstimate int,
	events chan<- agentEvent,
) {
	defer close(events)
	response, err := streamChatWithConversationRecovery(ctx, configuredClient, client.ChatRequest{
		Model: modelID, Messages: providerMessages(history, coalesceInstructions), ConversationID: conversationID,
	}, func(delta chatStreamDelta) bool {
		return emitAgentEvent(ctx, events, agentEvent{streamDelta: &delta})
	})
	emitAgentEvent(ctx, events, agentEvent{response: response, requestEstimate: requestEstimate, err: err})
}

func streamAgentLoop(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	modelID string,
	history []client.Message,
	conversationID string,
	activeTask *workspace.ActiveTask,
	streamEnabled bool,
	coalesceInstructions bool,
	events chan<- agentEvent,
) {
	defer close(events)
	availableTools := append(toolRuntime.Tools(), orchestrationTools()...)
	toolCalls := 0
	taskStarted := activeTask != nil
	if activeTask != nil {
		history = insertLeadingInstruction(history, activeTaskResumeMessage(*activeTask))
	}
	for round := 0; ; round++ {
		requestEstimate := memory.CountMessages(history)
		request := client.ChatRequest{
			Model: modelID, Messages: providerMessages(history, coalesceInstructions), ConversationID: conversationID, Tools: availableTools,
		}
		var response *client.ChatResponse
		var err error
		if streamEnabled {
			response, err = streamChatWithConversationRecovery(ctx, configuredClient, request, func(delta chatStreamDelta) bool {
				return emitAgentEvent(ctx, events, agentEvent{streamDelta: &delta})
			})
		} else {
			response, err = chatWithConversationRecovery(ctx, configuredClient, request)
		}
		if err != nil {
			emitAgentEvent(ctx, events, agentEvent{err: err})
			return
		}
		if response == nil || len(response.Choices) == 0 {
			emitAgentEvent(ctx, events, agentEvent{response: response})
			return
		}
		assistant := response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		if response.ConversationID != "" {
			conversationID = response.ConversationID
		}
		if len(assistant.ToolCalls) == 0 {
			if !taskStarted {
				response.Choices[0].Message = assistant
				response.ConversationID = conversationID
				emitAgentEvent(ctx, events, agentEvent{
					response: response, requestEstimate: requestEstimate, toolCalls: toolCalls,
				})
				return
			}
			history = append(history, assistant, client.Message{
				Role: client.RoleUser,
				Content: "This task was started with task_start and is not complete until you call task_complete. " +
					"Call task_complete now with the final outcome and summary; " +
					"if more work is required, continue the work first.",
			})
			continue
		}
		for index := range assistant.ToolCalls {
			if assistant.ToolCalls[index].ID == "" {
				assistant.ToolCalls[index].ID = fmt.Sprintf("q-call-%d-%d", round+1, index+1)
			}
		}
		history = append(history, assistant)
		if !emitAgentEvent(ctx, events, agentEvent{message: &assistant}) {
			return
		}
		for _, call := range assistant.ToolCalls {
			callCopy := call
			if !emitAgentEvent(ctx, events, agentEvent{call: &callCopy}) {
				return
			}
			if call.Function.Name == taskStartToolName {
				input, parseErr := parseTaskStart(call.Function.Arguments)
				if parseErr == nil && taskStarted {
					parseErr = errors.New("another task_start lifecycle is already active")
				}
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_start arguments: "+parseErr.Error(), true)
					history = append(history, message)
					if !emitAgentEvent(ctx, events, agentEvent{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				taskStarted = true
				body, _ := json.Marshal(taskStartOutput{Started: true, Objective: input.Objective})
				message := orchestrationToolResult(call, string(body), false)
				history = append(history, message)
				if !emitAgentEvent(ctx, events, agentEvent{message: &message}) {
					return
				}
				started := &workspace.ActiveTask{
					Objective: input.Objective, CompletionCriteria: append([]string(nil), input.CompletionCriteria...),
					StartedAt: time.Now().UTC(),
				}
				if !emitAgentEvent(ctx, events, agentEvent{taskStarted: started}) {
					return
				}
				continue
			}
			if call.Function.Name == askToUserToolName {
				input, parseErr := parseAskToUser(call.Function.Arguments)
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid ask_to_user arguments: "+parseErr.Error(), true)
					history = append(history, message)
					if !emitAgentEvent(ctx, events, agentEvent{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				answerChannel := make(chan askToUserOutput, 1)
				if !emitAgentEvent(ctx, events, agentEvent{question: &input, answer: answerChannel}) {
					return
				}
				var answer askToUserOutput
				select {
				case answer = <-answerChannel:
				case <-ctx.Done():
					return
				}
				if answer.Err != nil {
					emitAgentEvent(ctx, events, agentEvent{err: answer.Err})
					return
				}
				body, _ := json.Marshal(answer)
				message := orchestrationToolResult(call, string(body), false)
				history = append(history, message)
				if !emitAgentEvent(ctx, events, agentEvent{message: &message}) {
					return
				}
				continue
			}
			if call.Function.Name == taskCompleteToolName {
				if !taskStarted {
					message := orchestrationToolResult(call, "task_complete requires an active task_start lifecycle", true)
					history = append(history, message)
					if !emitAgentEvent(ctx, events, agentEvent{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				completion, parseErr := parseTaskComplete(call.Function.Arguments)
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_complete arguments: "+parseErr.Error(), true)
					history = append(history, message)
					if !emitAgentEvent(ctx, events, agentEvent{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				body, _ := json.Marshal(completion)
				message := orchestrationToolResult(call, string(body), false)
				history = append(history, message)
				if !emitAgentEvent(ctx, events, agentEvent{message: &message}) {
					return
				}
				if !emitAgentEvent(ctx, events, agentEvent{taskCompleted: true}) {
					return
				}
				if !emitAgentEvent(ctx, events, agentEvent{
					learningName: thinker.TaskCompleteEventName, learningPayload: append(json.RawMessage(nil), body...),
				}) {
					return
				}
				response.Choices[0].Message = client.Message{
					Role: client.RoleAssistant, Name: thinker.TaskCompletionReplyName,
					Content: renderTaskCompletion(completion),
				}
				response.ConversationID = conversationID
				emitAgentEvent(ctx, events, agentEvent{
					response: response, requestEstimate: requestEstimate, toolCalls: toolCalls,
				})
				return
			}
			result, callErr := toolRuntime.Call(ctx, call)
			if callErr != nil {
				result = client.ToolResult{Content: callErr.Error(), IsError: true}
			}
			content := result.Content
			if result.IsError {
				content = "Tool error: " + content
			}
			message := client.Message{
				Role: client.RoleTool, Name: call.Function.Name,
				ToolCallID: call.ID, Content: content,
			}
			history = append(history, message)
			toolCalls++
			if !emitAgentEvent(ctx, events, agentEvent{message: &message, toolIsError: result.IsError}) {
				return
			}
		}
	}
}

func orchestrationToolResult(call client.ToolCall, content string, isError bool) client.Message {
	if isError {
		content = "Tool error: " + content
	}
	return client.Message{
		Role: client.RoleTool, Name: call.Function.Name,
		ToolCallID: call.ID, Content: content,
	}
}

func activeTaskResumeMessage(task workspace.ActiveTask) client.Message {
	content := "A task from an earlier turn is still active. Continue it without calling task_start again, and call task_complete exactly once when it succeeds or is genuinely blocked. Objective: " + task.Objective
	if len(task.CompletionCriteria) > 0 {
		content += " Completion criteria: " + strings.Join(task.CompletionCriteria, "; ")
	}
	return client.Message{Role: client.RoleDeveloper, Content: content}
}

func insertLeadingInstruction(messages []client.Message, instruction client.Message) []client.Message {
	index := 0
	for index < len(messages) && (messages[index].Role == client.RoleSystem || messages[index].Role == client.RoleDeveloper) {
		index++
	}
	result := make([]client.Message, 0, len(messages)+1)
	result = append(result, messages[:index]...)
	result = append(result, instruction)
	return append(result, messages[index:]...)
}

func emitAgentEvent(ctx context.Context, events chan<- agentEvent, event agentEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitAgentEvent(events <-chan agentEvent, turnID uint64) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			event.err = errors.New("agent event stream closed unexpectedly")
		}
		return agentEventMsg{events: events, event: event, turnID: turnID}
	}
}

// providerMessages removes q-internal message names that some compatible APIs
// reject outside the user role. For OpenAI-compatible routes it also combines
// the leading system/developer instruction block because strict local chat
// templates commonly treat every developer message as a system message while
// allowing a system message only at the beginning.
func providerMessages(messages []client.Message, coalesceInstructions bool) []client.Message {
	result := append([]client.Message(nil), messages...)
	for index := range result {
		if result[index].Role != client.RoleUser {
			result[index].Name = ""
		}
	}
	if !coalesceInstructions {
		return result
	}
	leading := 0
	var instructions []string
	for leading < len(result) && (result[leading].Role == client.RoleSystem || result[leading].Role == client.RoleDeveloper) {
		if content := strings.TrimSpace(result[leading].TextContent()); content != "" {
			instructions = append(instructions, content)
		}
		leading++
	}
	if leading == 0 {
		return result
	}
	merged := client.Message{Role: client.RoleSystem, Content: strings.Join(instructions, "\n\n")}
	coalesced := make([]client.Message, 0, len(result)-leading+1)
	coalesced = append(coalesced, merged)
	return append(coalesced, result[leading:]...)
}

func (m *model) rollbackPendingMessage() {
	m.finishTurn()
	if m.pendingMessage.Content != "" {
		if len(m.messages) > 0 {
			m.messages = m.messages[:len(m.messages)-1]
		}
		if m.memory != nil {
			m.memory.PopLast()
		}
		m.input.SetValue(m.pendingMessage.Content)
	}
	m.pendingMessage = client.Message{}
	m.streamResponse = ""
	m.waiting = false
	m.compacting = false
	m.asking = false
	m.planArmed = false
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.compactionTarget = 0
	m.submitPending = false
	m.refreshTranscript()
}

func (m *model) enterChat(value config.Config, configuredClient chatClient) {
	m.screen = screenChat
	m.setupEdit = false
	m.config = value
	m.client = configuredClient
	workspaceModelErr := m.restoreWorkspaceModel()
	workspaceLearningErr := m.restoreWorkspaceLearning()
	active := m.activeConfig()
	m.messages = nil
	m.transcriptThoughts = nil
	m.streamResponse = ""
	if value.Provider.SystemPrompt != "" {
		m.messages = append(m.messages, client.Message{Role: client.RoleSystem, Content: value.Provider.SystemPrompt})
	}
	m.appendRuntimeMessages()
	m.memory = memory.New(memoryPolicy(active), m.messages)
	m.conversationID = ""
	m.runID = ""
	m.sessionTitle = ""
	m.sessionUpdatedAt = time.Time{}
	m.resetSessionLearning()
	m.waiting = false
	m.compacting = false
	m.asking = false
	m.planResumePending = false
	m.planCheckpoint = subagent.ExecutionCheckpoint{}
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.compactionTarget = 0
	m.submitPending = false
	m.pendingMessage = client.Message{}
	m.clearAgentActivities()
	m.status = ""
	m.input = newChatInput()
	m.restoreWorkspaceSession()
	if workspaceModelErr != nil {
		if m.status != "" {
			m.status += " · "
		}
		m.status += workspaceModelErr.Error()
	}
	if workspaceLearningErr != nil {
		if m.status != "" {
			m.status += " · "
		}
		m.status += workspaceLearningErr.Error()
	}
	m.ensureRunID()
	m.offerPlanExecutionResume()
	m.resize(m.width, m.height)
	m.input.Focus()
	m.refreshTranscript()
}

func (m *model) appendRuntimeMessages() {
	if m.toolRuntime != nil && m.workspaceStore != nil {
		environment := m.toolRuntime.Environment()
		workspacePrompt := fmt.Sprintf(
			"Runtime environment: OS=%s; architecture=%s; run_command shell=%s. Use commands and quoting compatible with this shell. ",
			environment.OS, environment.Architecture, environment.Shell,
		) + "Current workspace root: " + filepath.Clean(m.workspaceStore.Root) +
			". Use the available tools to inspect, edit, and run work in this workspace when the user asks for changes." +
			" For repository discovery, never traverse q's .q metadata directory and honor patterns in the workspace-root .qignore file, including when scanning through run_command. Explicit ignored-path access is allowed when the task requires it." +
			" Non-Loom MCP tool results include a loom_ref to the immutable full result. For large results, use loom_inspect, loom_read, or loom_eval instead of copying the result through chat context."
		if m.archive != nil {
			workspacePrompt += " Use search_archive when prior workspace conversations, decisions, agent results, or tool failures may be relevant; use get_archive_record only for selected results that need more detail."
		}
		m.messages = append(m.messages, client.Message{
			Role: client.RoleDeveloper, Name: "q_workspace",
			Content: workspacePrompt,
		})
	}
	if m.toolRuntime != nil {
		toolsAvailable := true
		if runtime, ok := m.toolRuntime.(*qtools.Runtime); ok && runtime == nil {
			toolsAvailable = false
		}
		var runtimeTools []client.Tool
		if toolsAvailable {
			runtimeTools = m.toolRuntime.Tools()
		}
		for _, tool := range runtimeTools {
			if tool.Function.Name == "search_skills" {
				m.messages = append(m.messages, client.Message{
					Role: client.RoleDeveloper, Name: "q_agent_skills",
					Content: "Agent Skills are retrieved on demand from the global q Library and the workspace skill index rather than preloaded. When reusable procedural guidance may help, call search_skills with concise keywords, select a result, then call get_skill and inspect its Loom artifact. Search explicit $skill-name mentions by name.",
				})
				break
			}
		}
		for _, tool := range runtimeTools {
			if tool.Function.Name == "search_propositions" {
				m.messages = append(m.messages, client.Message{
					Role: client.RoleDeveloper, Name: "q_propositions",
					Content: "Durable cross-workspace facts are stored as global q Library propositions. When a prior preference, decision, constraint, stable fact, or reusable resolution may be relevant, call search_propositions, then call get_proposition only for a selected result that needs full provenance or extraction metadata.",
				})
				break
			}
		}
		m.messages = append(m.messages, client.Message{
			Role: client.RoleDeveloper, Name: "q_orchestration",
			Content: "Use task_start before work that requires tools or multiple execution steps. Once task_start succeeds, that task must finish with exactly one successful task_complete call; do not finish it with a plain assistant response. " +
				"Direct questions and short answers may finish normally without task_start or task_complete. Use ask_to_user when a required user decision or missing detail prevents safe progress; wait for the answer and then continue the same turn. " +
				"Call task_complete only for a started task after all requested work and appropriate verification are done, or with outcome blocked when progress genuinely cannot continue.",
		})
	}
}

func (m *model) enterSetup(value config.Config) {
	m.screen = screenSetup
	m.setupEdit = true
	m.discovering = false
	m.setupFocus = setupBaseURL
	m.setup[setupBaseURL].SetValue(value.Provider.BaseURL)
	m.setup[setupAPIKeyEnv].SetValue(value.Provider.APIKeyEnv)
	m.setup[setupAPIKey].SetValue(value.Provider.APIKey)
	m.input.Blur()
	for index := range m.setup {
		m.setup[index].Blur()
	}
	m.status = ""
}

func (m *model) enterProviderList() {
	m.screen = screenProviders
	m.gatewayConfig = m.runtime.Config()
	if m.providerCursor >= len(m.gatewayConfig.Providers) {
		m.providerCursor = max(0, len(m.gatewayConfig.Providers)-1)
	}
	m.discovering = false
	m.input.Blur()
	for index := range m.setup {
		m.setup[index].Blur()
	}
	if len(m.gatewayConfig.Providers) == 0 {
		m.enterProviderEditor(-1)
		return
	}
	m.status = ""
}

func (m *model) enterProviderEditor(index int) {
	if index < 0 || index >= len(m.gatewayConfig.Providers) {
		index = -1
	}
	m.screen = screenSetup
	m.setupEdit = index >= 0
	m.providerAdding = index < 0
	m.providerEditIndex = index
	m.setupFocus = setupProviderID
	provider := gateway.ProviderConfig{
		ID: "provider", Type: "openai-compatible", Enabled: true,
		BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY",
	}
	if index >= 0 && index < len(m.gatewayConfig.Providers) {
		provider = m.gatewayConfig.Providers[index]
	}
	m.setup[setupProviderID].SetValue(provider.ID)
	m.setup[setupProviderPrefix].SetValue(provider.Prefix)
	m.setProviderType(provider.Type)
	m.setProviderKind(provider.Kind)
	m.setup[setupBaseURL].SetValue(provider.BaseURL)
	m.setup[setupAPIKeyEnv].SetValue(provider.APIKeyEnv)
	m.setup[setupAPIKey].SetValue(provider.APIKey)
	m.input.Blur()
	for fieldIndex := range m.setup {
		m.setup[fieldIndex].Blur()
	}
	m.setup[m.setupFocus].Focus()
	m.status = ""
}

func (m *model) enterModelPicker(value config.Config, available []client.Model) {
	byID := make(map[string]client.Model, len(available))
	for _, candidate := range available {
		if id := strings.TrimSpace(candidate.ID); id != "" {
			candidate.ID = id
			byID[id] = candidate
		}
	}
	m.models = make([]client.Model, 0, len(byID))
	for _, candidate := range byID {
		m.models = append(m.models, candidate)
	}
	sort.Slice(m.models, func(i, j int) bool { return m.models[i].ID < m.models[j].ID })
	if len(m.models) == 0 {
		m.status = "provider returned no models"
		m.screen = m.modelReturn
		return
	}
	m.screen = screenModels
	m.draftConfig = value
	m.modelCursor = 0
	m.modelFilter.Reset()
	m.status = ""
	if m.modelChooseTarget {
		m.modelPickerStage = modelPickerTargets
		m.modelTarget = defaultModelTarget
		m.modelTargetCursor = 0
		m.modelScopeCursor = modelScopeGlobal
		m.modelWorkspace = false
		m.modelFilter.Blur()
		return
	}
	m.modelPickerStage = modelPickerModels
	m.modelTarget = defaultModelTarget
	m.selectConfiguredModel()
	m.modelFilter.Focus()
}

func (m *model) selectConfiguredModel() {
	modelID := m.draftConfig.Provider.Model
	if m.modelWorkspace {
		if override, found := m.workspaceOverride(m.modelTarget); found {
			modelID = override.Model
		} else if m.modelTarget != defaultModelTarget {
			if agent, err := m.activeConfig().EffectiveAgent(m.modelTarget); err == nil {
				if agent.Group != "" {
					modelID = modelGroupChoicePrefix + agent.Group
				} else {
					modelID = agent.Model
				}
			}
		}
		if name, grouped := strings.CutPrefix(modelID, "group/"); grouped && m.modelTarget != defaultModelTarget {
			modelID = modelGroupChoicePrefix + name
		}
	} else if m.modelTarget == embeddingModelTarget {
		modelID = m.draftConfig.Embedding.Model
	} else if m.modelTarget != "" && m.modelTarget != defaultModelTarget {
		if agent, ok := m.draftConfig.Agents.Roles[m.modelTarget]; ok {
			if agent.Group != "" {
				modelID = modelGroupChoicePrefix + agent.Group
			} else if agent.Model != "" {
				modelID = agent.Model
			}
		}
	}
	m.modelCursor = 0
	for index, candidate := range m.selectableModels() {
		if candidate.ID == modelID {
			m.modelCursor = index
			return
		}
	}
}

func (m *model) modelPickerFocus() tea.Cmd {
	switch {
	case m.modelPickerStage == modelPickerModels && !m.discovering:
		return m.modelFilter.Focus()
	case m.modelPickerStage == modelPickerGroupName:
		return m.modelGroupNameInput.Focus()
	case m.modelPickerStage == modelPickerGroupCandidateModels:
		return m.modelFilter.Focus()
	case m.modelPickerStage == modelPickerGroupCandidateTimeout:
		return m.modelGroupTimeoutInput.Focus()
	case m.modelPickerStage == modelPickerEmbeddingDimensions && !m.discovering:
		return m.embeddingDimensions.Focus()
	case m.modelPickerStage == modelPickerContextWindow && !m.discovering:
		return m.modelContextWindow.Focus()
	}
	m.modelFilter.Blur()
	m.embeddingDimensions.Blur()
	m.modelContextWindow.Blur()
	m.modelGroupNameInput.Blur()
	m.modelGroupTimeoutInput.Blur()
	return nil
}

func (m model) modelTargets() []string {
	return append([]string{defaultModelTarget, embeddingModelTarget}, config.AgentRoles()...)
}

func (m model) saveWorkspaceModel(target string, selected client.Model) (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		m.status = "Workspace model settings are unavailable"
		return m, nil
	}
	if !workspace.ModelOverrideAllowed(target) {
		m.status = modelTargetLabel(target) + " cannot be overridden per workspace"
		return m, nil
	}
	modelID := selected.ID
	if name, grouped := modelGroupChoice(m.draftConfig, selected.ID); grouped {
		modelID = "group/" + name
	}
	value := cloneWorkspaceModelConfig(m.workspaceModel)
	if value.Overrides == nil {
		value.Overrides = make(map[string]workspace.ModelOverride)
	}
	value.Version = workspace.ModelConfigVersion
	value.Overrides[target] = workspace.ModelOverride{Model: modelID}
	if err := value.Validate(); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.status = "Saving workspace model…"
	m.modelFilter.Blur()
	store := *m.workspaceStore
	return m, func() tea.Msg {
		if err := store.SaveModelConfig(value); err != nil {
			return workspaceModelConfiguredMsg{err: err}
		}
		return workspaceModelConfiguredMsg{config: value, target: target}
	}
}

func (m model) clearWorkspaceModel(target string) (tea.Model, tea.Cmd) {
	if m.workspaceStore == nil {
		return m, nil
	}
	m.status = "Clearing workspace model…"
	store := *m.workspaceStore
	value := cloneWorkspaceModelConfig(m.workspaceModel)
	delete(value.Overrides, target)
	return m, func() tea.Msg {
		var err error
		if len(value.Overrides) == 0 {
			err = store.ClearModelConfig()
		} else {
			err = store.SaveModelConfig(value)
		}
		if err != nil {
			return workspaceModelConfiguredMsg{err: err}
		}
		return workspaceModelConfiguredMsg{
			config: value, target: target, cleared: true,
		}
	}
}

func cloneWorkspaceModelConfig(value workspace.ModelConfig) workspace.ModelConfig {
	result := value
	if value.Overrides != nil {
		result.Overrides = make(map[string]workspace.ModelOverride, len(value.Overrides))
		for role, override := range value.Overrides {
			result.Overrides[role] = override
		}
	}
	return result
}

func (m model) workspaceOverride(target string) (workspace.ModelOverride, bool) {
	override, found := m.workspaceModel.Overrides[target]
	return override, found
}

func withAgentModel(value config.Config, role, modelID string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	agent := value.Agents.Roles[role]
	agent.Model = modelID
	agent.Group = ""
	value.Agents.Roles[role] = agent
	return value
}

func withAgentGroup(value config.Config, role, group string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	agent := value.Agents.Roles[role]
	agent.Model = ""
	agent.Group = group
	agent.ReasoningEffort = ""
	value.Agents.Roles[role] = agent
	return value
}

func withAgentReasoningEffort(value config.Config, role, effort string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	agent := value.Agents.Roles[role]
	agent.ReasoningEffort = effort
	value.Agents.Roles[role] = agent
	return value
}

func withoutAgentOverride(value config.Config, role string) config.Config {
	value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
	delete(value.Agents.Roles, role)
	return value
}

func cloneAgentRoles(source map[string]config.AgentConfig) map[string]config.AgentConfig {
	result := make(map[string]config.AgentConfig, len(source)+1)
	for role, agent := range source {
		result[role] = agent
	}
	return result
}

func selectedReasoning(model client.Model) *client.ReasoningCapabilities {
	if model.Capabilities == nil || model.Capabilities.Reasoning == nil {
		return nil
	}
	reasoning := model.Capabilities.Reasoning
	if !reasoning.Supported || reasoning.Control != client.ReasoningControlEffort {
		return nil
	}
	return reasoning
}

func reasoningEffortCursor(value config.Config, role string, efforts []string) int {
	configured := value.Agents.Roles[role].ReasoningEffort
	for index, effort := range efforts {
		if configured == effort {
			return index + 1
		}
	}
	return 0
}

func (m model) filteredModels() []client.Model {
	available := m.selectableModels()
	query := strings.ToLower(strings.TrimSpace(m.modelFilter.Value()))
	if query == "" {
		return available
	}
	filtered := make([]client.Model, 0, len(available))
	for _, candidate := range available {
		if strings.Contains(strings.ToLower(candidate.ID), query) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (m model) selectableModels() []client.Model {
	if m.modelTarget == "" || m.modelTarget == defaultModelTarget {
		return append([]client.Model(nil), m.models...)
	}
	result := m.candidateModels()
	if m.modelTarget == embeddingModelTarget {
		return result
	}
	names := make([]string, 0, len(m.draftConfig.ModelGroups))
	for name := range m.draftConfig.ModelGroups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, client.Model{ID: modelGroupChoicePrefix + name})
	}
	return result
}

func (m model) candidateModels() []client.Model {
	result := make([]client.Model, 0, len(m.models))
	for _, model := range m.models {
		if !strings.HasPrefix(model.ID, "group/") {
			result = append(result, model)
		}
	}
	return result
}

func (m model) filteredCandidateModels() []client.Model {
	available := m.candidateModels()
	query := strings.ToLower(strings.TrimSpace(m.modelFilter.Value()))
	if query == "" {
		return available
	}
	result := make([]client.Model, 0, len(available))
	for _, model := range available {
		if strings.Contains(strings.ToLower(model.ID), query) {
			result = append(result, model)
		}
	}
	return result
}

func modelGroupChoice(value config.Config, choice string) (string, bool) {
	name, grouped := strings.CutPrefix(choice, modelGroupChoicePrefix)
	if !grouped {
		return "", false
	}
	_, found := value.ModelGroups[name]
	return name, found
}

func (m *model) resetConversation(runIDs ...string) {
	m.resetConversationState(runIDs...)
	if m.workspaceStore != nil {
		if err := errors.Join(m.workspaceStore.ClearExecution(), m.saveWorkspaceSession()); err != nil {
			m.status = err.Error()
		}
	}
	m.refreshTranscript()
}

func (m *model) resetConversationState(runIDs ...string) {
	m.messages = nil
	m.transcriptThoughts = nil
	m.streamResponse = ""
	if m.config.Provider.SystemPrompt != "" {
		m.messages = append(m.messages, client.Message{Role: client.RoleSystem, Content: m.config.Provider.SystemPrompt})
	}
	m.appendRuntimeMessages()
	active := m.activeConfig()
	if m.memory == nil {
		m.memory = memory.New(memoryPolicy(active), m.messages)
	} else {
		m.memory = memory.New(memoryPolicy(active), m.messages)
	}
	m.conversationID = ""
	m.activeTask = nil
	m.runID = ""
	if len(runIDs) > 0 {
		m.runID = strings.TrimSpace(runIDs[0])
	}
	m.sessionTitle = ""
	m.sessionUpdatedAt = time.Now().UTC()
	m.ensureRunID()
	m.resetSessionLearning()
	if err := m.ensureLearningMachine(thinker.LearningState{}, nil); err != nil {
		m.status = err.Error()
	}
	m.compactionTarget = 0
	m.submitPending = false
	m.asking = false
	m.slashCompletion = slashCompletionState{}
	m.planArmed = false
	m.planResumePending = false
	m.planCheckpoint = subagent.ExecutionCheckpoint{}
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.input.Placeholder = "Type a message…"
	m.status = "Conversation cleared"
	m.clearAgentActivities()
	m.resize(m.width, m.height)
}

// releaseConversationState drops the in-memory projection owned by a closed
// ACP session without changing its durable files or workspace-wide settings.
func (m *model) releaseConversationState(root string) {
	m.stopSessionLearning()
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.messages = nil
	m.memory = nil
	m.learning = nil
	m.conversationID = ""
	m.activeTask = nil
	m.pendingMessage = client.Message{}
	m.requestEstimate = 0
	m.compactionTarget = 0
	m.waiting = false
	m.compacting = false
	m.submitPending = false
	m.asking = false
	m.pendingQuestion = askToUserInput{}
	m.questionAnswer = nil
	m.questionEvents = nil
	m.questionTurnID = 0
	m.planArmed = false
	m.planResumePending = false
	m.planCheckpoint = subagent.ExecutionCheckpoint{}
	m.transcriptThoughts = nil
	m.streamResponse = ""
	m.turnContext = nil
	m.turnCancel = nil
	m.turnMessageStart = 0
	m.runID = ""
	m.sessionTitle = ""
	m.sessionUpdatedAt = time.Time{}
	m.clearAgentActivities()
	m.workspaceStore = &workspace.Store{Root: root}
	m.workspaceRestored = true
}

func (m *model) startNewWorkspaceSession() error {
	if m.workspaceStore == nil {
		return errors.New("workspace session storage is unavailable")
	}
	store, lock, err := workspace.CreateSession(m.workspaceStore.Root, "q")
	if err != nil {
		return err
	}
	previousLock := m.workspaceLock
	m.workspaceStore = &store
	m.workspaceLock = lock
	m.workspaceRestored = true
	m.resetConversation("run-" + store.SessionID)
	if previousLock != nil {
		if err := previousLock.Close(); err != nil {
			return err
		}
	}
	m.status = "New workspace session · " + store.SessionID
	return nil
}

func (m *model) stopSessionLearning() {
	if m.learningCancel != nil {
		m.learningCancel()
	}
	m.learningCtx = nil
	m.learningCancel = nil
	m.sessionGeneration++
	m.thinkerBusy = false
	m.thinkerJobID = ""
}

func (m *model) resetSessionLearning() {
	m.stopSessionLearning()
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	m.learningCtx, m.learningCancel = context.WithCancel(parent)
}

func (m *model) restoreWorkspaceSession() {
	if m.workspaceStore == nil || m.workspaceRestored {
		return
	}
	m.workspaceRestored = true
	session, err := m.workspaceStore.Load()
	if errors.Is(err, workspace.ErrNotFound) {
		if learningErr := m.ensureLearningMachine(thinker.LearningState{}, nil); learningErr != nil {
			m.status = learningErr.Error()
		}
		return
	}
	if err != nil {
		m.status = err.Error()
		return
	}
	base := append([]client.Message(nil), m.messages...)
	m.messages = mergeWorkspaceMessages(base, session.Transcript)
	requestContext := session.Context
	if len(requestContext) == 0 {
		requestContext = session.Transcript
	}
	m.memory = memory.New(memoryPolicy(m.activeConfig()), mergeWorkspaceMessages(base, requestContext))
	if learningErr := m.ensureLearningMachine(session.Learning, requestContext); learningErr != nil {
		m.status = learningErr.Error()
	}
	m.runID = session.RunID
	m.sessionTitle = session.Title
	if m.sessionTitle == "" {
		m.sessionTitle = sessionTitleFromMessages(session.Transcript)
	}
	if session.UpdatedAt != nil {
		m.sessionUpdatedAt = *session.UpdatedAt
	}
	m.activeTask = cloneActiveTask(session.ActiveTask)
	m.ensureRunID()
	if len(session.Transcript) > 0 {
		m.status = fmt.Sprintf("Workspace session restored · %d messages", len(session.Transcript))
		if m.activeTask != nil {
			m.status += " · active task resumed"
		}
	}
}

func (m *model) restoreWorkspaceModel() error {
	if m.workspaceStore == nil || m.workspaceModelRestored {
		return nil
	}
	m.workspaceModelRestored = true
	value, err := m.workspaceStore.LoadModelConfig()
	if err != nil {
		return err
	}
	m.workspaceModel = value
	return nil
}

func (m *model) restoreWorkspaceLearning() error {
	if m.workspaceStore == nil || m.workspaceLearningRestored {
		return nil
	}
	m.workspaceLearningRestored = true
	value, err := m.workspaceStore.LoadLearningConfig()
	if err != nil {
		m.workspaceLearning = workspace.LearningConfig{
			Version: workspace.LearningConfigVersion, Disabled: true,
		}
		return err
	}
	m.workspaceLearning = value
	return nil
}

// activeConfig applies the workspace's interactive model override while
// pinning shared-state model roles to their global inheritance. Embedding
// configuration is already independent from Provider.Model.
func (m model) activeConfig() config.Config {
	value := m.config
	if override, found := m.workspaceOverride(defaultModelTarget); found {
		value.Provider.Model = override.Model
		value.Provider.ContextWindow = m.contextWindowForModel(override.Model)
	}
	if len(m.workspaceModel.Overrides) > 0 {
		value.Agents.Roles = cloneAgentRoles(value.Agents.Roles)
		for role, override := range m.workspaceModel.Overrides {
			if role == defaultModelTarget || !workspace.ModelOverrideAllowed(role) {
				continue
			}
			agent := value.Agents.Roles[role]
			agent.Model = override.Model
			agent.Group = ""
			value.Agents.Roles[role] = agent
		}
		for _, role := range []string{config.AgentRoleThinker, config.AgentRoleLibrarian} {
			agent := value.Agents.Roles[role]
			if agent.Model == "" && agent.Group == "" {
				agent.Model = m.config.Provider.Model
				value.Agents.Roles[role] = agent
			}
		}
	}
	return value
}

func (m model) activeModel() string { return m.activeConfig().Provider.Model }

// contextWindowForModel resolves operational model metadata without storing a
// duplicate in workspace role overrides. Explicit Gateway metadata wins over
// the discovered catalog; the global selected-model cache is only a fallback.
func (m model) contextWindowForModel(modelID string) int64 {
	if contextWindow, configured := m.gatewayContextWindowOverride(modelID); configured {
		return contextWindow
	}
	for _, candidate := range m.models {
		if candidate.ID == modelID && candidate.ContextLength > 0 {
			return candidate.ContextLength
		}
	}
	if modelID == m.config.Provider.Model {
		return m.config.Provider.ContextWindow
	}
	return 0
}

func (m *model) saveWorkspaceSession() error {
	if m.workspaceStore == nil || m.memory == nil {
		return nil
	}
	return m.workspaceStore.Save(workspace.Session{
		RunID:      m.runID,
		Title:      m.sessionTitle,
		UpdatedAt:  workspaceTimePointer(m.sessionUpdatedAt),
		Transcript: workspaceSessionMessages(m.messages),
		Context:    workspaceSessionMessages(m.memory.Messages()),
		ActiveTask: cloneActiveTask(m.activeTask),
		Learning: func() thinker.LearningState {
			if m.learning == nil {
				return thinker.LearningState{}
			}
			return m.learning.State()
		}(),
	})
}

func workspaceTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func cloneActiveTask(task *workspace.ActiveTask) *workspace.ActiveTask {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.CompletionCriteria = append([]string(nil), task.CompletionCriteria...)
	return &cloned
}

func workspaceSessionMessages(messages []client.Message) []client.Message {
	saved := make([]client.Message, 0, len(messages))
	for _, message := range messages {
		if (message.Role == client.RoleSystem || message.Role == client.RoleDeveloper) && message.Name != memory.SummaryName {
			continue
		}
		saved = append(saved, message)
	}
	return saved
}

func mergeWorkspaceMessages(base, saved []client.Message) []client.Message {
	merged := append([]client.Message(nil), base...)
	for _, message := range saved {
		if (message.Role == client.RoleSystem || message.Role == client.RoleDeveloper) && message.Name != memory.SummaryName {
			continue
		}
		merged = append(merged, message)
	}
	return merged
}

func (m *model) resize(width, height int) {
	m.width, m.height = max(width, 40), max(height, 12)
	contentWidth := max(20, m.width-4)
	for index := range m.setup {
		m.setup[index].SetWidth(min(contentWidth-2, 72))
	}
	m.modelFilter.SetWidth(min(contentWidth-2, 72))
	m.embeddingDimensions.SetWidth(min(contentWidth-2, 24))
	m.modelContextWindow.SetWidth(min(contentWidth-2, 28))
	m.modelGroupNameInput.SetWidth(min(contentWidth-2, 40))
	m.modelGroupTimeoutInput.SetWidth(min(contentWidth-2, 48))
	m.skillsInput.SetWidth(min(contentWidth-10, 96))
	for index := range m.lspInputs {
		m.lspInputs[index].SetWidth(min(contentWidth-12, 84))
	}
	for index := range m.mcpInputs {
		m.mcpInputs[index].SetWidth(min(contentWidth-12, 84))
	}
	for index := range m.agentsInputs {
		m.agentsInputs[index].SetWidth(min(contentWidth-12, 84))
	}
	ignorePanel := ignoreEditorPanelStyle(max(36, m.width-4), m.dark)
	m.ignoreEditor.SetWidth(max(20, ignorePanel.GetWidth()-ignorePanel.GetHorizontalFrameSize()))
	m.ignoreEditor.SetHeight(max(4, m.height-16))
	helpPanel := helpPanelStyle(max(36, m.width-4), m.dark)
	m.helpViewport.SetWidth(max(20, helpPanel.GetWidth()-helpPanel.GetHorizontalFrameSize()))
	m.helpViewport.SetHeight(max(1, m.height-11))
	m.refreshHelp(false)
	m.refreshSkills(false)
	// Match the frame's inner width so it cannot rewrap the transcript or
	// textarea after their cursor and popup positions have been calculated.
	chatContentWidth := m.chatFrameWidth() - frameStyle.GetHorizontalFrameSize()
	m.input.SetWidth(chatContentWidth)
	m.viewport.SetWidth(chatContentWidth)
	tracePanel := agentTracePanelStyle(m.chatFrameWidth(), m.dark)
	m.agentTraceViewport.SetWidth(max(10, tracePanel.GetWidth()-tracePanel.GetHorizontalFrameSize()))
	chromeHeight := 10
	if m.workspaceStore != nil || m.clientIsACPRemote() {
		chromeHeight += 3
	}
	dynamicHeight := max(2, m.height-chromeHeight)
	m.agentLogVisible = 0
	if m.agentTraceExpanded && len(m.agentTraces) > 0 && dynamicHeight >= 8 {
		traceHeight := max(4, dynamicHeight*2/3)
		if m.asking {
			traceHeight = max(3, dynamicHeight/3)
		}
		m.agentTraceViewport.SetHeight(traceHeight)
		traceChrome := 3
		if m.asking {
			traceChrome++ // visual gap between execution trace and user decision
		}
		dynamicHeight = max(2, dynamicHeight-traceHeight-traceChrome)
		m.refreshAgentTrace(false)
	} else if !m.agentTraceExpanded && len(m.agentTraces) > 0 && !m.waiting {
		dynamicHeight = max(2, dynamicHeight-2)
	} else if len(m.agentActivities) > 0 && dynamicHeight >= 6 {
		m.agentLogVisible = min(len(m.agentActivities), max(1, min(8, dynamicHeight/4)))
		activityChrome := 2
		if m.asking {
			activityChrome++
		}
		dynamicHeight = max(2, dynamicHeight-m.agentLogVisible-activityChrome)
	}
	if m.asking {
		question := renderQuestionPanel(m.pendingQuestion, m.questionChoice, max(36, contentWidth), m.dark)
		questionHeight := min(lipgloss.Height(question), max(2, dynamicHeight*2/3))
		m.questionViewport.SetHeight(max(1, questionHeight-1)) // sticky semantic title occupies one row
		m.configureQuestionViewport(max(36, contentWidth))
		m.viewport.SetHeight(max(1, dynamicHeight-questionHeight-1))
	} else {
		m.questionViewport.SetContent("")
		m.viewport.SetHeight(max(3, dynamicHeight))
	}
	m.refreshTranscript()
}

func (m *model) applyColorScheme(dark bool) {
	m.dark = dark
	for index := range m.setup {
		m.setup[index].SetStyles(textinput.DefaultStyles(dark))
	}
	m.modelFilter.SetStyles(textinput.DefaultStyles(dark))
	m.embeddingDimensions.SetStyles(textinput.DefaultStyles(dark))
	m.modelContextWindow.SetStyles(textinput.DefaultStyles(dark))
	m.modelGroupNameInput.SetStyles(textinput.DefaultStyles(dark))
	m.modelGroupTimeoutInput.SetStyles(textinput.DefaultStyles(dark))
	for index := range m.lspInputs {
		m.lspInputs[index].SetStyles(textinput.DefaultStyles(dark))
	}
	for index := range m.mcpInputs {
		m.mcpInputs[index].SetStyles(textinput.DefaultStyles(dark))
	}
	for index := range m.agentsInputs {
		m.agentsInputs[index].SetStyles(textinput.DefaultStyles(dark))
	}
	inputStyles := textarea.DefaultStyles(dark)
	inputStyles.Cursor.Shape = tea.CursorBar
	m.input.SetStyles(inputStyles)
	m.ignoreEditor.SetStyles(inputStyles)
	m.refreshTranscript()
	m.refreshQuestion()
	m.refreshHelp(false)
}

func (m *model) refreshTranscript() {
	if m.screen != screenChat {
		return
	}
	m.viewport.SetContent(strings.Join(m.transcriptBlocks(), "\n\n"))
	m.viewport.GotoBottom()
}

func (m *model) refreshQuestion() {
	if m.screen != screenChat || !m.asking {
		m.questionViewport.SetContent("")
		return
	}
	m.configureQuestionViewport(m.chatFrameWidth())
}

func (m *model) configureQuestionViewport(width int) {
	panelStyle := questionPanelStyle(width, m.dark, isPlanControlQuestion(m.pendingQuestion))
	// Keep the viewport responsible only for clipping and scrolling. Applying the
	// panel frame to it makes bubbles calculate soft wraps with the outer width,
	// then Lip Gloss renders those lines into the narrower framed width. The
	// resulting second wrap can change the rendered height while scrolling.
	m.questionViewport.SetWidth(panelStyle.GetWidth() - panelStyle.GetHorizontalFrameSize())
	m.questionViewport.Style = lipgloss.NewStyle()
	m.questionViewport.StyleLineFunc = nil
	m.questionViewport.SetContent(renderPendingQuestion(m.pendingQuestion, m.questionChoice))
}

func (m model) renderedQuestionViewport() string {
	style := questionPanelStyle(m.chatFrameWidth(), m.dark, isPlanControlQuestion(m.pendingQuestion))
	title := lipgloss.NewStyle().Bold(true).Foreground(questionPanelAccent(m.pendingQuestion, m.dark))
	return style.Render(title.Render(questionPanelTitle(m.pendingQuestion)) + "\n" + m.questionViewport.View())
}

func (m *model) clearAgentActivities() {
	m.agentActivities = nil
	m.agentStates = nil
	m.agentLogVisible = 0
	m.agentTraces = nil
	m.agentTraceExpanded = true
	m.agentTraceViewport.SetContent("")
}

func (m *model) appendAgentTrace(trace agentTrace) {
	trace.Agent = strings.TrimSpace(trace.Agent)
	trace.TaskID = strings.TrimSpace(trace.TaskID)
	trace.ParentID = strings.TrimSpace(trace.ParentID)
	trace.Kind = strings.TrimSpace(trace.Kind)
	trace.Name = strings.TrimSpace(trace.Name)
	trace.Content = boundedAgentTraceContent(trace.Content)
	if trace.Agent == "" || trace.Kind == "" {
		return
	}
	wasAtBottom := len(m.agentTraces) == 0 || m.agentTraceViewport.AtBottom()
	m.agentTraces = append(m.agentTraces, trace)
	if len(m.agentTraces) > 400 {
		m.agentTraces = append([]agentTrace(nil), m.agentTraces[len(m.agentTraces)-400:]...)
	}
	m.refreshAgentTrace(wasAtBottom)
}

func boundedAgentTraceContent(content string) string {
	const maximum = 16 << 10
	content = strings.TrimSpace(content)
	if len(content) <= maximum {
		return content
	}
	return content[:maximum] + "\n… trace content truncated; full value remains in the session archive"
}

func (m *model) refreshAgentTrace(gotoBottom bool) {
	if m.screen != screenChat {
		return
	}
	m.agentTraceViewport.SetContent(strings.Join(m.agentTraceBlocks(), "\n\n"))
	if gotoBottom {
		m.agentTraceViewport.GotoBottom()
	}
}

func (m *model) appendAgentActivity(activity agentActivity) {
	activity.Agent = strings.TrimSpace(activity.Agent)
	activity.TaskID = strings.TrimSpace(activity.TaskID)
	activity.ParentID = strings.TrimSpace(activity.ParentID)
	activity.Action = strings.TrimSpace(activity.Action)
	activity.Detail = strings.TrimSpace(activity.Detail)
	if activity.Agent == "" {
		activity.Agent = "agent"
	}
	m.agentActivities = append(m.agentActivities, activity)
	if len(m.agentActivities) > 200 {
		m.agentActivities = append([]agentActivity(nil), m.agentActivities[len(m.agentActivities)-200:]...)
	}
	if activity.Agent == "plan" {
		return
	}
	if m.agentStates == nil {
		m.agentStates = make(map[string]string)
	}
	switch activity.Action {
	case "completed":
		m.agentStates[activity.Agent] = "succeeded"
	case "failed":
		m.agentStates[activity.Agent] = "failed"
	case "waiting":
		m.agentStates[activity.Agent] = "waiting"
	default:
		m.agentStates[activity.Agent] = "running"
	}
}

func activityStatus(activity agentActivity) string {
	activity.Agent = strings.TrimSpace(activity.Agent)
	if activity.Agent == "" {
		activity.Agent = "agent"
	}
	label := strings.ToUpper(activity.Agent[:1]) + activity.Agent[1:]
	detail := activity.Action
	if activity.Detail != "" {
		detail += " · " + activity.Detail
	}
	return label + " · " + detail
}

func (m model) renderedAgentActivities() string {
	if m.agentTraceExpanded && len(m.agentTraces) > 0 {
		contentWidth := max(20, m.chatFrameWidth()-frameStyle.GetHorizontalFrameSize())
		current := m.agentTraces[len(m.agentTraces)-1]
		header := agentTraceTitleStyle(m.dark).Render("SUBAGENT TRACE")
		summary := ansi.Truncate(header+subtleStyle.Render(" · ")+agentSummary(m.agentStates), contentWidth, "…")
		currentLabel := ansi.Truncate("› "+agentTraceLabel(current), contentWidth, "…")
		return summary + "\n" + agentTraceActiveStyle(m.dark).Render(currentLabel) + "\n" +
			agentTracePanelStyle(m.chatFrameWidth(), m.dark).Render(m.agentTraceViewport.View())
	}
	if !m.agentTraceExpanded && len(m.agentTraces) > 0 && !m.waiting {
		contentWidth := max(20, m.chatFrameWidth()-frameStyle.GetHorizontalFrameSize())
		summary := titleStyle.Render("SUBAGENTS COMPLETE") +
			subtleStyle.Render(" · ctrl+g inspect trace · ") + agentSummary(m.agentStates)
		return ansi.Truncate(summary, contentWidth, "…")
	}
	if m.agentLogVisible <= 0 || len(m.agentActivities) == 0 {
		return ""
	}
	var body strings.Builder
	contentWidth := max(20, m.chatFrameWidth()-frameStyle.GetHorizontalFrameSize())
	body.WriteString(ansi.Truncate(agentSummary(m.agentStates), contentWidth, "…"))
	body.WriteString("\n")
	start := max(0, len(m.agentActivities)-m.agentLogVisible)
	for index, activity := range m.agentActivities[start:] {
		active := start+index == len(m.agentActivities)-1 && m.waiting
		prefix, stageStyle := "· ", subtleStyle
		if active {
			prefix, stageStyle = "› ", activeLabelStyle
		}
		body.WriteString(stageStyle.Render(prefix))
		body.WriteString(stageStyle.Render(fmt.Sprintf("%-8s", activity.Agent)))
		body.WriteString(" ")
		detail := activity.Action
		if activity.Detail != "" {
			detail += " · " + activity.Detail
		}
		body.WriteString(subtleStyle.Render(ansi.Truncate(detail, max(4, contentWidth-12), "…")))
		if index < m.agentLogVisible-1 {
			body.WriteString("\n")
		}
	}
	return body.String()
}

func agentSummary(states map[string]string) string {
	parts := []string{titleStyle.Render("agents")}
	for _, role := range []string{"griller", "scout", "search", "planner", "coder", "executor"} {
		state, exists := states[role]
		if !exists {
			continue
		}
		marker := "…"
		switch state {
		case "succeeded":
			marker = "✓"
		case "failed":
			marker = "×"
		case "waiting":
			marker = "waiting"
		}
		style := subtleStyle
		if state == "running" || state == "waiting" {
			style = activeLabelStyle
		} else if state == "failed" {
			style = errorStyle
		}
		parts = append(parts, style.Render(role+" "+marker))
	}
	return strings.Join(parts, subtleStyle.Render(" · "))
}

func renderAgentTraceBlocks(traces []agentTrace, dark bool, width int, collapsed bool) []string {
	blocks := make([]string, 0, len(traces))
	for _, trace := range traces {
		style := subtleStyle
		if trace.IsError {
			style = errorStyle
		} else if trace.Kind == "tool_call" {
			style = activeLabelStyle
		}
		label := agentTraceLabel(trace)
		content := trace.Content
		if collapsed && trace.Kind == "tool_result" {
			label = "▸ " + label
			if trace.IsError && !strings.HasPrefix(content, "Tool error: ") {
				content = "Tool error: " + content
			}
			content = renderToolResult(client.Message{Name: trace.Name, Content: content}, dark, width, true)
		} else {
			content = formatAgentTraceContent(content)
		}
		block := style.Render(label)
		if content != "" {
			block += "\n" + content
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func agentTraceLabel(trace agentTrace) string {
	label := trace.Agent + " · " + strings.ReplaceAll(trace.Kind, "_", " ")
	if trace.Name != "" {
		label += " · " + trace.Name
	}
	return label
}

func formatAgentTraceContent(content string) string {
	var value any
	if json.Unmarshal([]byte(content), &value) == nil {
		if formatted, err := json.MarshalIndent(value, "", "  "); err == nil {
			return string(formatted)
		}
	}
	return content
}

func isQuestionScrollKey(key string) bool {
	switch key {
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		return true
	default:
		return false
	}
}

func isAgentTraceScrollKey(key string) bool {
	switch key {
	case "pgup", "pgdown", "ctrl+u", "ctrl+d", "home", "end":
		return true
	default:
		return false
	}
}

func (m model) View() tea.View {
	content := m.viewSetup()
	if m.screen == screenProviders {
		content = m.viewProviders()
	} else if m.screen == screenGateway {
		content = m.viewGateway()
	} else if m.screen == screenGatewayNetwork {
		content = m.viewGatewayNetwork()
	} else if m.screen == screenGatewayKeys {
		content = m.viewGatewayKeys()
	} else if m.screen == screenLibrary {
		content = m.viewLibrary()
	} else if m.screen == screenModels {
		content = m.viewModels()
	} else if m.screen == screenLoom {
		content = m.viewLoom()
	} else if m.screen == screenIgnore {
		content = m.viewIgnore()
	} else if m.screen == screenSkills {
		content = m.viewSkills()
	} else if m.screen == screenLSP {
		content = m.viewLSP()
	} else if m.screen == screenMCP {
		content = m.viewMCP()
	} else if m.screen == screenAgents {
		content = m.viewAgents()
	} else if m.screen == screenHelp {
		content = m.viewHelp()
	} else if m.screen == screenSessions {
		content = m.viewSessions()
	} else if m.screen == screenChat {
		content = m.viewChat()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "q"
	if m.screen == screenChat && m.asking {
		view.MouseMode = tea.MouseModeCellMotion
	}
	if m.screen == screenChat && ((!m.waiting && !m.initializing) || m.asking) {
		if cursor := m.input.Cursor(); cursor != nil {
			cursor.Position.X += frameStyle.GetPaddingLeft()
			cursor.Position.Y += frameStyle.GetPaddingTop() + m.chatInputOffset()
			view.Cursor = cursor
		}
	}
	return view
}

func normalizeTextInputMessage(message tea.Msg) tea.Msg {
	switch value := message.(type) {
	case tea.KeyPressMsg:
		if value.Text != "" && !norm.NFC.IsNormalString(value.Text) {
			value.Text = norm.NFC.String(value.Text)
		}
		return value
	case tea.PasteMsg:
		if !norm.NFC.IsNormalString(value.Content) {
			value.Content = norm.NFC.String(value.Content)
		}
		return value
	default:
		return message
	}
}

func (m model) chatHeader() string {
	if remote, ok := m.client.(*acpRemoteClient); ok {
		remote.mu.Lock()
		display := remote.display
		command := remote.command
		root := remote.root
		title := remote.title
		remote.mu.Unlock()
		header := titleStyle.Render("q") + "  " + subtleStyle.Render("ACP client · "+display+" · "+command)
		if title != "" {
			header += "\n" + subtleStyle.Render("session · "+title)
		}
		return header + "\n" + subtleStyle.Render("workspace · "+filepath.Clean(root))
	}
	endpoint := m.config.Provider.BaseURL
	if m.runtime != nil {
		endpoint = m.runtime.Endpoint()
	}
	header := titleStyle.Render("q") + "  " + subtleStyle.Render(m.activeModel()+" · "+endpoint+" · "+m.contextLabel())
	if m.workspaceStore != nil {
		header += "\n" + subtleStyle.Render("workspace · "+filepath.Clean(m.workspaceStore.Root))
	}
	return header
}

func (m model) clientIsACPRemote() bool {
	_, ok := m.client.(*acpRemoteClient)
	return ok
}

func (m model) chatFrameWidth() int {
	return max(36, m.width-4)
}

func (m model) renderedChatHeaderHeight() int {
	rendered := frameStyle.Width(m.chatFrameWidth()).Render(m.chatHeader())
	return lipgloss.Height(rendered) - frameStyle.GetVerticalFrameSize()
}

func (m model) chatInputOffset() int {
	// Measure the same prefix as the chat view, including any wrapping. The
	// marker occupies the first input row, which is not part of the offset.
	prefix := frameStyle.Width(m.chatFrameWidth()).Render(m.chatInputPrefix() + "x")
	return lipgloss.Height(prefix) - frameStyle.GetVerticalFrameSize() - 1
}

func (m model) chatInputPrefix() string {
	content := m.chatHeader() + "\n\n" + m.viewport.View() + "\n"
	if activities := m.renderedAgentActivities(); activities != "" {
		content += activities + "\n"
		if m.asking {
			content += "\n"
		}
	}
	if m.asking {
		content += m.renderedQuestionViewport() + "\n"
	}
	return content
}

func (m model) viewChat() string {
	status := m.status
	if (m.waiting || m.initializing) && !m.asking {
		status = m.spinner.View() + " " + status
	}
	footerText := "enter send · shift+enter newline · ctrl+h help · ctrl+c quit · esc quit"
	if m.initializing {
		footerText = "starting workspace services · ctrl+h help · ctrl+c quit · esc quit"
	} else if m.waiting {
		footerText = "ctrl+h help · ctrl+c interrupt turn · esc quit"
		if len(m.agentTraces) > 0 {
			if m.agentTraceExpanded {
				footerText = "pgup/pgdn trace · ctrl+g collapse trace · ctrl+h help · ctrl+c interrupt turn · esc quit"
			} else {
				footerText = "ctrl+g expand trace · ctrl+h help · ctrl+c interrupt turn · esc quit"
			}
		}
	} else if len(m.agentTraces) > 0 {
		if m.agentTraceExpanded {
			footerText = "pgup/pgdn trace · ctrl+g collapse trace · enter send · ctrl+h help · ctrl+c quit"
		} else {
			footerText = "ctrl+g expand trace · enter send · shift+enter newline · ctrl+h help · ctrl+c quit"
		}
	}
	if m.clientIsACPRemote() && !m.waiting && !m.initializing {
		footerText = "enter send · shift+enter newline · ctrl+l new ACP session · ctrl+h help · ctrl+c quit"
	}
	content := m.chatInputPrefix()
	if m.asking {
		if len(m.pendingQuestion.Choices) > 0 {
			if m.pendingQuestion.ChoiceOnly {
				footerText = "↑/↓ select · enter choose · pgup/pgdn review · ctrl+h help · ctrl+c interrupt"
			} else {
				footerText = "↑/↓ select · enter choose · type for custom answer · pgup/pgdn review · ctrl+h help · ctrl+c interrupt"
			}
		} else {
			footerText = "pgup/pgdn scroll · enter answer · shift+enter newline · ctrl+h help · ctrl+c interrupt"
		}
	}
	if !m.clientIsACPRemote() {
		action := "collapse"
		if m.toolResultsCollapsed {
			action = "expand"
		}
		footerText = strings.Replace(footerText, "ctrl+h help", "ctrl+o "+action+" tools · ctrl+h help", 1)
	}
	if len(m.slashCompletionMatches()) > 0 {
		footerText = "↑/↓ select · tab/enter complete · esc close · ctrl+h help"
	}
	footer := helpStyle.Render(ansi.Truncate(footerText, max(10, m.chatFrameWidth()-frameStyle.GetHorizontalFrameSize()), "…"))
	content += m.input.View()
	if status != "" {
		statusWidth := max(10, m.chatFrameWidth()-frameStyle.GetHorizontalFrameSize())
		content += "\n" + subtleStyle.Render(ansi.Truncate(status, statusWidth, "…"))
	}
	content += "\n" + footer
	return m.overlaySlashCompletion(frameStyle.Width(m.chatFrameWidth()).Render(content))
}

func (m model) writeWorkspacePath(body *strings.Builder) {
	if m.workspaceStore != nil {
		body.WriteString(subtleStyle.Render("workspace · " + filepath.Clean(m.workspaceStore.Root)))
		body.WriteString("\n")
	}
}

func memoryPolicy(value config.Config) memory.Policy {
	contextConfig := value.EffectiveContext()
	return memory.Policy{
		ContextWindow: int(value.EffectiveContextWindow()),
		TriggerRatio:  contextConfig.TriggerRatio,
		TargetRatio:   contextConfig.TargetRatio,
		RecentRatio:   contextConfig.RecentRatio,
	}
}

func (m model) contextLabel() string {
	if m.memory == nil {
		return "context unknown"
	}
	stats := m.memory.Stats()
	if stats.ContextWindow <= 0 {
		return "context unknown"
	}
	percent := min(999, stats.PredictedTokens*100/stats.ContextWindow)
	return fmt.Sprintf("context %d%% · %s/%s", percent, formatTokens(stats.PredictedTokens), formatTokens(stats.ContextWindow))
}

func formatTokens(tokens int) string {
	return formatTokenCount(int64(tokens))
}

func promptCacheStatus(usage client.Usage) string {
	if usage.PromptTokens <= 0 {
		return ""
	}
	if usage.PromptDetails == nil {
		return fmt.Sprintf("Prompt cache not reported · prompt %s", formatTokens(usage.PromptTokens))
	}
	cached := max(0, usage.PromptDetails.CachedTokens)
	percent := cached * 100 / usage.PromptTokens
	return fmt.Sprintf("Prompt cache %d%% · %s/%s", percent, formatTokens(cached), formatTokens(usage.PromptTokens))
}

func formatTokenCount(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

var (
	titleStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	subtleStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	helpStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	activeLabelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	userLabelStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	assistantLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	userBodyStyle       = lipgloss.NewStyle().PaddingLeft(1)
	assistantBodyStyle  = lipgloss.NewStyle().PaddingLeft(1)
	emptyStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	frameStyle          = lipgloss.NewStyle().Padding(1, 2)
)
