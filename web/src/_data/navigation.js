const structure = [
  {
    key: "gettingStarted",
    items: [
      { key: "introduction", path: "/guide/introduction/", icon: "document" },
      { key: "install", path: "/guide/install/", icon: "download" },
      { key: "firstRun", path: "/guide/first-run/", icon: "terminal" },
      { key: "providers", path: "/guide/providers/", icon: "nodes" },
    ],
  },
  {
    key: "workflows",
    items: [
      { key: "planning", path: "/workflows/planning/", icon: "play" },
      { key: "subagents", path: "/workflows/subagents/", icon: "agents" },
      { key: "agentSkills", path: "/workflows/agent-skills/", icon: "grid" },
      { key: "reviewCommit", path: "/workflows/review-and-commit/", icon: "document" },
      { key: "acpMcp", path: "/workflows/acp-and-mcp/", icon: "nodes" },
      { key: "remoteApi", path: "/workflows/remote-api/", icon: "cloud" },
    ],
  },
  {
    key: "concepts",
    items: [
      { key: "runtime", path: "/concepts/runtime/", icon: "graph" },
      { key: "sessions", path: "/concepts/sessions/", icon: "archive" },
      { key: "workspaceGuidance", path: "/concepts/workspace-guidance/", icon: "document" },
      { key: "security", path: "/concepts/security/", icon: "gate" },
    ],
  },
  {
    key: "reference",
    items: [
      { key: "commands", path: "/reference/commands/", icon: "command" },
      { key: "configuration", path: "/reference/configuration/", icon: "settings" },
    ],
  },
];

const labels = {
  en: {
    gettingStarted: "Getting started", introduction: "Introduction", install: "Install q", firstRun: "First run", providers: "Choose a provider",
    workflows: "Workflows", planning: "Plan and execute", subagents: "Subagents", agentSkills: "Agent Skills", reviewCommit: "Review and commit", acpMcp: "ACP and MCP", remoteApi: "Remote API",
    concepts: "Concepts", runtime: "Runtime model", sessions: "Sessions and memory", workspaceGuidance: "Workspace guidance", security: "Security boundaries",
    reference: "Reference", commands: "Commands", configuration: "Configuration",
  },
  ko: {
    gettingStarted: "시작하기", introduction: "소개", install: "q 설치", firstRun: "첫 실행", providers: "공급자 선택",
    workflows: "워크플로", planning: "계획 및 실행", subagents: "서브에이전트", agentSkills: "에이전트 스킬", reviewCommit: "변경 검토 및 커밋", acpMcp: "ACP와 MCP", remoteApi: "원격 API",
    concepts: "개념", runtime: "런타임 모델", sessions: "세션과 메모리", workspaceGuidance: "워크스페이스 지침", security: "보안 경계",
    reference: "참조", commands: "명령어", configuration: "구성",
  },
  ja: {
    gettingStarted: "はじめに", introduction: "概要", install: "q のインストール", firstRun: "初回実行", providers: "プロバイダーの選択",
    workflows: "ワークフロー", planning: "計画と実行", subagents: "サブエージェント", agentSkills: "エージェントスキル", reviewCommit: "変更確認とコミット", acpMcp: "ACP と MCP", remoteApi: "リモート API",
    concepts: "コンセプト", runtime: "ランタイムモデル", sessions: "セッションとメモリ", workspaceGuidance: "ワークスペース指示", security: "セキュリティ境界",
    reference: "リファレンス", commands: "コマンド", configuration: "設定",
  },
  "zh-cn": {
    gettingStarted: "快速开始", introduction: "简介", install: "安装 q", firstRun: "首次运行", providers: "选择提供商",
    workflows: "工作流", planning: "规划与执行", subagents: "子代理", agentSkills: "代理技能", reviewCommit: "审查与提交", acpMcp: "ACP 与 MCP", remoteApi: "远程 API",
    concepts: "概念", runtime: "运行时模型", sessions: "会话与记忆", workspaceGuidance: "工作区指引", security: "安全边界",
    reference: "参考", commands: "命令", configuration: "配置",
  },
};

const prefixFor = (locale) => locale === "en" ? "" : `/${locale}`;

export const navigationFor = (locale = "en") => {
  const selected = labels[locale] || labels.en;
  const prefix = prefixFor(locale);
  return structure.map((group) => ({
    label: selected[group.key],
    items: group.items.map((item) => ({ title: selected[item.key], url: `${prefix}${item.path}`, icon: item.icon })),
  }));
};

export default navigationFor("en");
