package app

import (
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/thinker"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
	"golang.org/x/text/unicode/norm"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	screenCustom
	screenGateway
	screenGatewayNetwork
	screenGatewayKeys
	screenLibrary
	screenHelp
	screenSessions
	screenChanges
	screenChat
)

type modelPickerStage uint8

const (
	modelPickerModels modelPickerStage = iota
	modelPickerTargets
	modelPickerRoleName
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

type configuredMsg struct {
	config          config.Config
	client          chatClient
	preserveHistory bool
	err             error
}

type archiveEmbeddingConfiguredMsg struct {
	stats        archiveembed.BackfillStats
	globalSkills int
	err          error
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

type modelAPIModeConfiguredMsg struct {
	config  config.Config
	client  chatClient
	modelID string
	err     error
}

type modelRoleConfiguredMsg struct {
	config config.Config
	target string
	status string
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

// AgentEvent reports progress and results from RunAgentLoop. Embedders should
// inspect events through the exported accessor methods.
type AgentEvent struct {
	status          string
	activity        *agentActivity
	trace           *agentTrace
	plan            *agentPlanUpdate
	message         *client.Message
	call            *client.ToolCall
	question        *askToUserInput
	answer          chan<- askToUserOutput
	toolIsError     bool
	compaction      *agentContextCompaction
	response        *client.ChatResponse
	complete        bool
	outcome         string
	requestEstimate int
	toolCalls       int
	learningName    string
	learningPayload json.RawMessage
	streamDelta     *chatStreamDelta
	taskStarted     *workspace.ActiveTask
	taskCompleted   bool
	contextReplace  *agentContextReplacement
	err             error
}

type agentEvent = AgentEvent

// ErrInteractionUnavailable indicates that an interactive tool cannot obtain
// input from the embedding host.
var ErrInteractionUnavailable = agentloop.ErrInteractionUnavailable

var errRemoteInteractionUnavailable = ErrInteractionUnavailable

// AgentContextReplacement replaces a message in the embedding host's retained
// transcript after the loop has repaired its local context.
type AgentContextReplacement = agentloop.ContextReplacement

type agentContextReplacement = AgentContextReplacement

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
	CallID   string
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
	checkpointStore   *workspace.Store
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
		hostState: hostState{ctx: ctx, store: store, factory: factory, runtime: runtime, screen: screenSetup},
		chatState: chatState{
			viewport:           viewport.New(viewport.WithWidth(80), viewport.WithHeight(12)),
			questionViewport:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(8)),
			agentTraceViewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(8)),
			helpViewport:       viewport.New(viewport.WithWidth(80), viewport.WithHeight(12)),
			changes:            newChangesViewState(),
			spinner:            spinner.New(spinner.WithSpinner(spinner.Dot)),
		},
		lifecycleState: lifecycleState{thinkerSerial: &thinker.Serial{}},
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
	m.modelRoleNameInput = textinput.New()
	m.modelRoleNameInput.Prompt = "role name · "
	m.modelRoleNameInput.Placeholder = "security-review"
	m.modelRoleNameInput.CharLimit = 64
	m.modelRoleNameInput.SetWidth(40)
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
	// Terminals with keyboard-disambiguation support report Shift+Enter as a
	// modified Enter key. Others commonly encode the same gesture as LF, which
	// Bubble Tea exposes as Ctrl+J. Accept both representations so the shortcut
	// works without depending on a specific terminal keyboard protocol.
	input.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
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
	if m.screen == screenCustom && m.custom.connections && m.agentsMode != agentsModeList {
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
	case changesLoadedMsg:
		return m.receiveChanges(message)
	case changePreviewMsg:
		return m.receiveChangePreview(message)
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
		m.toolRuntime = nil
		if message.tools != nil {
			m.toolRuntime = message.tools
		}
		m.setArchiveWriter(message.archive)
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
			statuses = append(statuses, "Warning: workspace archive unavailable (archive search and indexing disabled): "+message.archiveErr.Error())
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
		if message.result.LogError != "" {
			m.archiveFailure("thinker log", errors.New(message.result.LogError))
			_ = m.flushArchive()
		}
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
		if message.checkpointStore != nil {
			if err := message.checkpointStore.ClearThinkerCheckpoint(message.jobID); err != nil {
				m.archiveFailure("clear thinker checkpoint", err)
				_ = m.flushArchive()
				m.status = "Thinker checkpoint cleanup: " + err.Error()
				m.resize(m.width, m.height)
				return m, m.startNextLearningSegment()
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
			m.clearResponseReplay()
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
		} else if message.stats.Embedded > 0 || message.globalSkills > 0 {
			m.status = fmt.Sprintf(
				"Embedded %d workspace record(s) and %d global skill(s)",
				message.stats.Embedded, message.globalSkills,
			)
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
		if message.target == embeddingModelTarget && m.client != nil {
			return m, m.configureEmbeddingRuntime(message.config, m.client)
		}
		return m, nil
	case modelAPIModeConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelFilter.Focus()
		}
		if m.client != nil && m.client != message.client {
			_ = m.client.Close()
		}
		m.config, m.draftConfig, m.client = message.config, message.config, message.client
		m.conversationID = ""
		m.clearResponseReplay()
		m.status = message.modelID + " · " + m.preferredModelAPIMode(message.modelID)
		return m, m.modelFilter.Focus()
	case modelRoleConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelPickerFocus()
		}
		m.config = message.config
		m.draftConfig = message.config
		m.refreshModelGroupCatalog()
		m.screen = screenModels
		m.modelPickerStage = modelPickerTargets
		m.modelRoleDeleteArmed = false
		m.modelRoleNameInput.Blur()
		m.selectModelTarget(message.target)
		m.status = message.status
		return m, nil
	case workspaceModelConfiguredMsg:
		if message.err != nil {
			m.status = message.err.Error()
			return m, m.modelPickerFocus()
		}
		m.workspaceModel = message.config
		m.conversationID = ""
		m.clearResponseReplay()
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
		m.clearResponseReplay()
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
		return m.updateAgentEvent(message)
	case chatResultMsg:
		return m.updateChatResult(message)
	case compactionResultMsg:
		return m.updateCompactionResult(message)
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

	return m.updateScreenMessage(message)
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
	root := ""
	if m.workspaceStore != nil {
		root = m.workspaceStore.Root
	}
	m.messages = PrepareWorkspaceMessages(m.messages, WorkspaceMessageOptions{
		Root: root, Tools: m.toolRuntime, ArchiveAvailable: m.archive != nil,
	})
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
	m.modelRoleDeleteArmed = false
	m.modelRoleNameInput.Blur()
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
	case m.modelPickerStage == modelPickerRoleName:
		return m.modelRoleNameInput.Focus()
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
	m.modelRoleNameInput.Blur()
	m.embeddingDimensions.Blur()
	m.modelContextWindow.Blur()
	m.modelGroupNameInput.Blur()
	m.modelGroupTimeoutInput.Blur()
	return nil
}

func (m model) modelTargets() []string {
	return append([]string{defaultModelTarget, embeddingModelTarget}, m.activeConfig().NativeRoles()...)
}

func (m *model) selectModelTarget(target string) {
	m.modelTargetCursor = 0
	for index, candidate := range m.modelTargets() {
		if candidate == target {
			m.modelTargetCursor = index
			return
		}
	}
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
		maps.Copy(result.Overrides, value.Overrides)
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

func withModelReasoningEffort(value config.Config, role, effort string) config.Config {
	if role == "" || role == defaultModelTarget {
		value.Provider.ReasoningEffort = effort
		return value
	}
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
	maps.Copy(result, source)
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
	if role == "" || role == defaultModelTarget {
		configured = value.Provider.EffectiveReasoningEffort()
	}
	return effortCursor(configured, efforts)
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
		err := errors.Join(m.workspaceStore.ClearExecution(), m.saveWorkspaceSession())
		if err == nil {
			err = m.workspaceStore.ClearThinkerCheckpointAny()
		}
		if err != nil {
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
	transcript, interruptedCalls := reconcileInterruptedToolCalls(session.Transcript)
	m.messages = mergeWorkspaceMessages(base, transcript)
	requestContext := session.Context
	if len(requestContext) == 0 {
		requestContext = session.Transcript
	}
	requestContext = restoreResponseReplay(requestContext, session.ResponseReplay)
	var contextInterruptedCalls int
	requestContext, contextInterruptedCalls = reconcileInterruptedToolCalls(requestContext)
	m.memory = memory.New(memoryPolicy(m.activeConfig()), mergeWorkspaceMessages(base, requestContext))
	if learningErr := m.ensureLearningMachine(session.Learning, requestContext); learningErr != nil {
		m.status = learningErr.Error()
	}
	m.runID = session.RunID
	if affinity := session.ResponseAffinity; affinity != nil &&
		affinity.Model == m.activeModel() && strings.HasPrefix(affinity.Key, "cache_") &&
		m.activeModelAPIMode(affinity.Model) == "responses" {
		m.conversationID = affinity.Key
	}
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
	if interruptedCalls > 0 || contextInterruptedCalls > 0 {
		m.status += " · interrupted tool call recovered"
		if err := m.saveWorkspaceSession(); err != nil {
			m.status += " · " + err.Error()
		}
	}
}

func reconcileInterruptedToolCalls(messages []client.Message) ([]client.Message, int) {
	var recovered []client.Message
	interrupted := 0
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		recovered = append(recovered, message)
		if len(message.ToolCalls) == 0 {
			continue
		}
		completed := make(map[string]bool, len(message.ToolCalls))
		for index+1 < len(messages) && messages[index+1].Role == client.RoleTool {
			index++
			result := messages[index]
			recovered = append(recovered, result)
			completed[result.ToolCallID] = true
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || completed[call.ID] {
				continue
			}
			recovered = append(recovered, client.Message{
				Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID,
				Content: "Tool error: interrupted by session restart; execution outcome unknown",
			})
			interrupted++
		}
	}
	return recovered, interrupted
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

type workspaceSessionSaveError struct {
	err error
}

func (e *workspaceSessionSaveError) Error() string { return e.err.Error() }
func (e *workspaceSessionSaveError) Unwrap() error { return e.err }

func (m *model) saveWorkspaceSession() error {
	if m.workspaceStore == nil || m.memory == nil {
		return nil
	}
	requestContext := workspaceSessionMessages(m.memory.Messages())
	var responseAffinity *workspace.ResponseAffinity
	if strings.HasPrefix(m.conversationID, "cache_") && m.activeModelAPIMode(m.activeModel()) == "responses" {
		responseAffinity = &workspace.ResponseAffinity{Model: m.activeModel(), Key: m.conversationID}
	}
	err := m.workspaceStore.Save(workspace.Session{
		RunID:            m.runID,
		Title:            m.sessionTitle,
		UpdatedAt:        workspaceTimePointer(m.sessionUpdatedAt),
		Transcript:       workspaceSessionMessages(m.messages),
		Context:          requestContext,
		ResponseReplay:   collectResponseReplay(requestContext),
		ResponseAffinity: responseAffinity,
		ActiveTask:       cloneActiveTask(m.activeTask),
		Learning: func() thinker.LearningState {
			if m.learning == nil {
				return thinker.LearningState{}
			}
			return m.learning.State()
		}(),
	})
	if err != nil {
		return &workspaceSessionSaveError{err: err}
	}
	return nil
}

func (m *model) clearResponseReplay() {
	if m.memory != nil {
		m.memory.ClearResponseReplay()
	}
	for index := range m.messages {
		m.messages[index].ResponseOutput = nil
		m.messages[index].ResponseModel = ""
	}
}

func collectResponseReplay(messages []client.Message) []workspace.ResponseReplayItem {
	var replay []workspace.ResponseReplayItem
	for index, message := range messages {
		if len(message.ResponseOutput) > 0 {
			replay = append(replay, workspace.ResponseReplayItem{Index: index, Model: message.ResponseModel, Output: message.ResponseOutput})
		}
	}
	return replay
}

func restoreResponseReplay(messages []client.Message, replay []workspace.ResponseReplayItem) []client.Message {
	if len(replay) == 0 {
		return messages
	}
	restored := append([]client.Message(nil), messages...)
	for _, item := range replay {
		if item.Index >= 0 && item.Index < len(restored) && restored[item.Index].Role == client.RoleAssistant {
			restored[item.Index].ResponseOutput = append([]json.RawMessage(nil), item.Output...)
			restored[item.Index].ResponseModel = item.Model
		}
	}
	return restored
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
	for i := range m.custom.inputs {
		m.custom.inputs[i].SetWidth(max(20, contentWidth-8))
	}
	if m.custom.editing {
		m.custom.prompt.SetWidth(max(20, contentWidth-8))
		m.custom.prompt.SetHeight(max(3, min(10, m.height-15)))
	}
	if m.custom.picker {
		m.custom.filter.SetWidth(max(20, contentWidth-8))
	}
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
	m.resizeChanges()
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
	for i := range m.custom.inputs {
		m.custom.inputs[i].SetStyles(textinput.DefaultStyles(dark))
	}
	if m.custom.editing {
		m.custom.prompt.SetStyles(textarea.DefaultStyles(dark))
	}
	if m.custom.picker {
		m.custom.filter.SetStyles(textinput.DefaultStyles(dark))
	}
	m.dark = dark
	for index := range m.setup {
		m.setup[index].SetStyles(textinput.DefaultStyles(dark))
	}
	m.modelFilter.SetStyles(textinput.DefaultStyles(dark))
	m.modelRoleNameInput.SetStyles(textinput.DefaultStyles(dark))
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
	m.resizeChanges()
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
		switch state {
		case "running", "waiting":
			style = activeLabelStyle
		case "failed":
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
	switch m.screen {
	case screenProviders:
		content = m.viewProviders()
	case screenGateway:
		content = m.viewGateway()
	case screenGatewayNetwork:
		content = m.viewGatewayNetwork()
	case screenGatewayKeys:
		content = m.viewGatewayKeys()
	case screenLibrary:
		content = m.viewLibrary()
	case screenModels:
		content = m.viewModels()
	case screenLoom:
		content = m.viewLoom()
	case screenIgnore:
		content = m.viewIgnore()
	case screenSkills:
		content = m.viewSkills()
	case screenLSP:
		content = m.viewLSP()
	case screenMCP:
		content = m.viewMCP()
	case screenCustom:
		content = m.viewCustom()
	case screenHelp:
		content = m.viewHelp()
	case screenSessions:
		content = m.viewSessions()
	case screenChanges:
		content = m.viewChanges()
	case screenChat:
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
			cursor.X += frameStyle.GetPaddingLeft()
			cursor.Y += frameStyle.GetPaddingTop() + m.chatInputOffset()
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
		header += "\n" + subtleStyle.Render("workspace · "+filepath.Clean(root))
		if warning := m.archiveUnavailableWarning(); warning != "" {
			header += "\n" + errorStyle.Render(warning)
		}
		return header
	}
	endpoint := m.config.Provider.BaseURL
	if m.runtime != nil {
		endpoint = m.runtime.Endpoint()
	}
	header := titleStyle.Render("q") + "  " + subtleStyle.Render(m.activeModel()+" · "+endpoint+" · "+m.contextLabel())
	if m.workspaceStore != nil {
		header += "\n" + subtleStyle.Render("workspace · "+filepath.Clean(m.workspaceStore.Root))
	}
	if warning := m.archiveUnavailableWarning(); warning != "" {
		header += "\n" + errorStyle.Render(warning)
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
