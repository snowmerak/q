export default {
  en: {
    heading: "A workspace-native coding environment.", lead: "Run sessions, follow delegated work, review changes, and configure q from one local Studio.", start: "Get started →", github: "View on GitHub",
    terminalLabel: "Delegated work visible in Q Studio", copy: "Copy",
    sessionTree: { root: true, state: "is-active", agent: "Studio migration", detail: "~/q · root session", status: "Running", children: [
      { state: "is-done", agent: "Manager", detail: "Defined milestones and acceptance criteria", status: "Done" },
      { state: "is-done", agent: "Research", detail: "Compared relevant approaches", status: "Done" },
      { state: "is-active", agent: "Senior developer", detail: "Reviewing the implementation", status: "Running", children: [
        { state: "is-active", agent: "Junior developer", detail: "Editing code and running checks", status: "Running" },
      ] },
    ] },
    featureHeading: "One Studio, every workspace", featureLead: "Start work in any repository, keep long-running sessions visible, and move from request to reviewed change in the browser.",
    features: [
      { icon: "scope", title: "Run durable sessions", body: "Register root sessions from different repositories and keep chat, context usage, and run controls together." },
      { icon: "delegate", title: "Follow every handoff", body: "Open each delegated child as a nested session, inspect its tools and transcript, and intervene when direction changes." },
      { icon: "remember", title: "Review and operate", body: "Inspect repository diffs, prepare commits, configure providers and roles, and monitor local services from Studio." },
    ],
    workflowHeading: "Visible work across roles", workflowLead: "This is one possible handoff. The main agent chooses specialists as needed, and the senior developer reviews implementation results.",
    steps: ["Request", "Assign", "Implement", "Review", "Report"], ctaHeading: "Open Q Studio.", ctaBody: "Install q, start Studio, register a repository, and send the first request.", cta: "Open the guide →",
  },
  ko: {
    heading: "워크스페이스 중심 코딩 환경.", lead: "하나의 로컬 Studio에서 세션을 실행하고, 위임을 따라가고, 변경을 검토하고, q를 구성하세요.", start: "시작하기 →", github: "GitHub에서 보기",
    terminalLabel: "Q Studio에 표시된 위임 작업", copy: "복사",
    sessionTree: { root: true, state: "is-active", agent: "Studio 마이그레이션", detail: "~/q · 루트 세션", status: "실행 중", children: [
      { state: "is-done", agent: "Manager", detail: "마일스톤과 수용 기준을 정의함", status: "완료" },
      { state: "is-done", agent: "Research", detail: "관련 접근 방식을 비교함", status: "완료" },
      { state: "is-active", agent: "Senior developer", detail: "구현 결과를 검토하는 중", status: "실행 중", children: [
        { state: "is-active", agent: "Junior developer", detail: "코드를 수정하고 검사하는 중", status: "실행 중" },
      ] },
    ] },
    featureHeading: "하나의 Studio에서 모든 워크스페이스를", featureLead: "어떤 저장소에서든 작업을 시작하고, 장기 실행 세션을 확인하며, 요청부터 검토된 변경까지 브라우저에서 이어가세요.",
    features: [
      { icon: "scope", title: "영구 세션 실행", body: "서로 다른 저장소의 루트 세션을 등록하고 채팅, 컨텍스트 사용량과 실행 제어를 한곳에서 관리합니다." },
      { icon: "delegate", title: "모든 위임 추적", body: "위임된 자식을 중첩 세션으로 열어 도구와 대화를 확인하고, 방향이 바뀌면 직접 개입합니다." },
      { icon: "remember", title: "검토와 운영", body: "저장소 diff와 커밋을 검토하고, 공급자와 역할을 구성하며, 로컬 서비스 상태를 Studio에서 확인합니다." },
    ],
    workflowHeading: "역할 사이의 작업을 한눈에", workflowLead: "이 흐름은 위임의 한 예시입니다. 메인 에이전트가 필요한 전문가를 선택하고 senior developer가 구현 결과를 검토합니다.",
    steps: ["요청", "할당", "구현", "검토", "보고"], ctaHeading: "Q Studio를 여세요.", ctaBody: "q를 설치하고 Studio를 시작한 뒤 저장소를 등록하고 첫 요청을 보내세요.", cta: "가이드 열기 →",
  },
  ja: {
    heading: "ワークスペースネイティブな開発環境。", lead: "一つのローカル Studio でセッション、委任、変更レビュー、q の設定を管理します。", start: "はじめる →", github: "GitHub で見る",
    terminalLabel: "Q Studio に表示された委任作業", copy: "コピー",
    sessionTree: { root: true, state: "is-active", agent: "Studio の移行", detail: "~/q · ルートセッション", status: "実行中", children: [
      { state: "is-done", agent: "Manager", detail: "マイルストーンと受け入れ基準を定義", status: "完了" },
      { state: "is-done", agent: "Research", detail: "関連するアプローチを比較", status: "完了" },
      { state: "is-active", agent: "Senior developer", detail: "実装結果をレビュー中", status: "実行中", children: [
        { state: "is-active", agent: "Junior developer", detail: "コードを変更して検証中", status: "実行中" },
      ] },
    ] },
    featureHeading: "一つの Studio ですべてのワークスペースを", featureLead: "任意のリポジトリで作業を始め、長時間セッションを追跡し、依頼からレビュー済み変更までブラウザで進めます。",
    features: [
      { icon: "scope", title: "永続セッションを実行", body: "異なるリポジトリのルートセッションを登録し、チャット、コンテキスト使用量、実行制御をまとめて管理します。" },
      { icon: "delegate", title: "すべての委任を追跡", body: "委任した子を入れ子のセッションとして開き、ツールと会話を確認し、方向が変われば介入できます。" },
      { icon: "remember", title: "レビューと運用", body: "diff とコミットをレビューし、プロバイダーとロールを設定し、ローカルサービスを Studio から監視します。" },
    ],
    workflowHeading: "役割をまたぐ作業を可視化", workflowLead: "これは委任の一例です。メインエージェントが必要な専門家を選び、senior developer が実装結果をレビューします。",
    steps: ["依頼", "割り当て", "実装", "レビュー", "報告"], ctaHeading: "Q Studio を開きましょう。", ctaBody: "q をインストールし、Studio を起動してリポジトリを登録し、最初の依頼を送信します。", cta: "ガイドを開く →",
  },
  "zh-cn": {
    heading: "工作区原生开发环境。", lead: "在一个本地 Studio 中运行会话、跟踪委派、审查变更并配置 q。", start: "开始使用 →", github: "在 GitHub 上查看",
    terminalLabel: "Q Studio 中显示的委派工作", copy: "复制",
    sessionTree: { root: true, state: "is-active", agent: "Studio 迁移", detail: "~/q · 根会话", status: "运行中", children: [
      { state: "is-done", agent: "Manager", detail: "定义里程碑和验收标准", status: "完成" },
      { state: "is-done", agent: "Research", detail: "比较相关方案", status: "完成" },
      { state: "is-active", agent: "Senior developer", detail: "正在审查实现结果", status: "运行中", children: [
        { state: "is-active", agent: "Junior developer", detail: "修改代码并运行检查", status: "运行中" },
      ] },
    ] },
    featureHeading: "一个 Studio，管理所有工作区", featureLead: "在任意仓库开始工作，持续查看长时间运行的会话，并在浏览器中完成从请求到变更审查的流程。",
    features: [
      { icon: "scope", title: "运行持久会话", body: "注册不同仓库的根会话，并集中管理聊天、上下文用量和运行控制。" },
      { icon: "delegate", title: "跟踪每次委派", body: "将委派的子任务作为嵌套会话打开，查看工具和对话，并在方向变化时进行干预。" },
      { icon: "remember", title: "审查与运维", body: "审查仓库 diff 与提交，配置提供商和角色，并从 Studio 监控本地服务。" },
    ],
    workflowHeading: "角色间的工作清晰可见", workflowLead: "这是一种委派示例。主代理按需选择专家，senior developer 审查实现结果。",
    steps: ["请求", "分配", "实施", "审查", "报告"], ctaHeading: "打开 Q Studio。", ctaBody: "安装 q，启动 Studio，注册仓库，然后发送第一个请求。", cta: "打开指南 →",
  },
};
