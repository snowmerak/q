---
locale: zh-cn
title: 代理技能
description: 为 q 提供仅在当前任务相关时才检索的便携式指令。
sectionLabel: 指南
toc:
  - id: 检索方式
    label: 检索方式
  - id: 发现位置
    label: 发现位置
  - id: 编写技能
    label: 编写技能
  - id: 管理技能
    label: 管理技能
  - id: 刷新与索引
    label: 刷新与索引
---

## 检索方式

q 将便携式的 [`SKILL.md` Agent Skills 格式](https://agentskills.io/specification)实现为按需检索层。完整技能目录和正文不会进入基础提示。

新用户轮次、`task_start` 或 `ask_to_user` 返回的回答都可能触发技能元数据搜索，q 最多添加四个此前未提示的候选。提示本身不是指令；模型必须调用 `get_skill` 后才能遵循技能。

拥有技能工具的角色还可以在新信息显示需要额外指导时调用 `search_skills`。

词法排名依次提高技能名称、标签和描述的权重。配置嵌入后，q 融合 BM25 与向量候选。当语义候选可信而词法分数只是噪声时，q 可以忽略弱 BM25 贡献，避免语言不匹配埋没强语义结果。语义可信度不足时，完整词法分支仍会保留。

## 发现位置

q 按优先级从低到高读取：

```text
~/.agents/skills/
~/.q/skills/
<nearest-git-root>/.agents/skills/
<workspace>/.agents/skills/
<workspace>/.q/skills/
```

只有最近的 Git 根目录不同于当前工作区时，才使用其技能目录。因此从子目录启动 q 时，既能发现仓库的便携式技能，也不会扩大文件工具的工作区边界。

如果两个有界结果集中出现同名技能，则优先使用工作区定义。

## 编写技能

创建包含 `SKILL.md` 的目录：

```markdown
---
name: release-check
description: 发布前验证候选版本。
---

# 发布检查

阅读发布清单，运行文档中的验证步骤，并报告证据。
```

frontmatter 中的 `name` 是规范名称，但不必与目录同名。`description` 可省略。请将正文限定在确实会改变工作结果的场景。

## 管理技能

打开 `/skills` 或运行 `q skills`。两个面板对应 q 管理的目录：

```text
GLOBAL    -> ~/.q/skills/<skill-name>
WORKSPACE -> <workspace>/.q/skills/<skill-name>
```

左右方向键或 Tab 切换范围，上下方向键选择。`A` 克隆 Git 仓库，`U` 快进更新，`D` 在确认后删除，`R` 重新发现所有目录并协调索引。

`.agents/skills` 中的技能会显示在目录里，但由于这些目录不归 q 所有，只能查看。受管理的仓库必须在根目录有 `SKILL.md`，目标名称来自经过验证的 frontmatter，而非仓库 URL。

## 刷新与索引

未配置嵌入模型时，只使用 BM25 元数据搜索。在 `/model` 中分配模型后，q 结合 BM25 和 HNSW 向量结果。分配新嵌入模型会重建向量投影，并为已有技能补建向量。

q 运行期间，如果距离上次检查至少经过 30 秒，下一次使用技能会重新协调工作区技能目录。可以通过 `/skills` 管理 q 拥有的技能并显式重建索引。
