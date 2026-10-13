# i18n 批量抽取 — 两波完成(2026-10-13)

> Wave 1:6 worker(主表面)。Wave 2:4 worker(深扫残留:deep-scan 覆盖 toast/三元字符串/
> 对象字面量/aria/跨行 JSX 等此前漏报的模式)。
> 全部通过 `lib/i18n/parity.test.ts`、`tsc --noEmit`、全量 vitest(1173/1173)。

## Wave 2 成果(Run `run_3d75432031e8`)

| Worker | 命名空间            | 新增键                   | 内容                                                                                                                                                                                                      |
| ------ | ------------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| WA     | logs + errors       | samples/agentHandover 等 | 3 个 emptyState 样例提示、otelView toast、agentHandoverView(原本零 i18n)、接通 4 个既有未用键                                                                                                             |
| WB     | playground          | breadcrumbAriaLabel 等   | skillEditForm/shared 的 aria;**查明 docs 页 12 条标题只用于生成 data-testid(刻意英文)**                                                                                                                   |
| WC     | governance          | (复用既有键)             | virtualKeySheet 清掉死字段;tree 页 metadata 惰性 getter 接既有键                                                                                                                                          |
| WD     | workspaces          | ~120                     | skills/prompts 全套、providerSelector/modelSelector/multiSelect/entityAssociationSelect、providerConfigCard 摘要、celBuilder、budgetOverride、overheadBreakdown、clientLayout、**pprof 页 + devProfiler** |
| 协调者 | auth/mcp/playground | +11                      | loginView 页脚链接(Discord/GitHub/Docs)、agentSheet 摘要、agent-gateway toast、fileManagerView 草稿节点名                                                                                                 |

## 深扫残留收敛

- 激进扫描:45 文件 → **20 文件(全部已验证豁免)**
- 保守扫描:A 类 58→20、B 类 51→8(剩余同上豁免集)

## 最终豁免清单(已逐个人工核实,不再处理)

1. **data-testid 源**:docs/page 标题、agentFilterSidebar、modelLimitsTable、betaHeaders、virtualKeySheet 模板(源码有注释标记刻意英文)
2. **产品名**:icons.tsx svg 标题(Python/LangChain/Mistral AI…)、harnesses/platformSelect(Claude Code/Cursor/VS Code/Windows)、skillListView、observabilityView 插件名、agentCardPreview OAuth 规范字段、OTel/Prometheus/Datadog/Kafka 等
3. **Trans 组件回退文案**:mcpClientSheet "Allow by Default"、mcpUsageGuideSheet "MCP settings"
4. **getter defaultValue 英文回退**:loginView 链接、routing tree 页(既定 fallback 模式)
5. **技术标识**:x-bf-\* header 示例、X-Bifrost-Setup-Token、SYNC_INTERVAL_REQUIRED 错误码、GOMAXPROCS/GC、HTTP+JSON、"Jan 20" 日期样例
6. **品牌/徽章**:BETA、Esc、"Powered by"、modelSelector "\*" 常量(渲染处已翻译)

## 总账(两波合计)

- 组件文件:约 145 个;locale 键新增约 480 对(en+zh)
- 8 个 locale 命名空间被扩充:logs、governance、mcp、errors、auth、providers、workspaces、playground(+ common/settings 小幅)
- 验证基线:vitest 1173/1173、parity en≡zh-CN、tsc 0 错误