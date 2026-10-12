# Bifrost Web UI 多语言支持（英文 + 简体中文）设计文档

日期：2026-10-12
状态：已批准（设计经用户逐节确认）

## 1. 目标与范围

为 Bifrost Web UI（`ui/`）增加 i18n 基础设施，实现英文 + 简体中文全量双语。

- **范围**：全部 38 个 workspace 路由区域 + 共享组件（~489 个页面文件、~184 个组件文件，约 303 个文件含界面文案）
- **默认语言**：英文。首次访问按浏览器语言自动检测（`navigator.language` 以 `zh` 开头 → 中文），检测失败回退英文
- **偏好持久化**：localStorage，key 为 `bifrost-language`。不依赖登录态（setup 页面在认证前也需可用）
- **手动切换**：Topbar 右侧用户区语言切换器，热切换无需刷新
- **明确不翻译**：后端数据保持原文 —— provider 名称、模型名、日志内容、API 返回的错误信息、MCP 工具描述

非目标：不做其他语言（法语/日语等，架构天然可扩展但不本次实施）；不持久化语言偏好到后端用户档案（localStorage 已覆盖当前需求，未来可加）。

## 2. 架构

### 2.1 目录结构

```
ui/lib/i18n/
├── index.ts          # i18next 实例初始化（默认导出 i18n 实例）
├── detection.ts      # 语言检测：localStorage → navigator.language → "en"
├── lazyResources.ts  # 按命名空间 + 语言动态 import() 语言包
├── parity.test.ts    # en/zh-CN 键结构一致性测试
└── locales/
    ├── en/           # common.json, dashboard.json, governance.json, ...
    └── zh-CN/        # 与 en 同构镜像
```

### 2.2 初始化

- 在 `ui/app/main.tsx` 中 `createRoot` 渲染之前 `import "@/lib/i18n"`（副作用导入，同步初始化实例）
- 不使用 `I18nextProvider`：react-i18next 全局单例 + `useTranslation` hook 即可，避免在 `clientLayout.tsx` 增加包裹层
- 检测逻辑自研（~20 行，不引入 `i18next-browser-languagedetector`）：
  1. `localStorage.getItem("bifrost-language")`，合法值（`en` / `zh-CN`）直接用
  2. 否则 `navigator.language` 以 `zh` 开头 → `zh-CN`
  3. 否则 `en`

### 2.3 语言包加载

- 语言包按命名空间懒加载：自定义轻量 backend（`lazyResources.ts`），`useTranslation(ns)` 触发对应命名空间的加载；当前语言为 `zh-CN` 时，同一命名空间的 `en` 包**并行预载**（它是 `fallbackLng` 兜底源），两者都就绪后 `t()` 才输出译文，不存在「zh 到了 en 没到」的中间态
- JSON 直接用 Vite 原生 JSON import，无额外构建配置；缺失 key 由 `parseMissingKeyHandler` 在开发环境打印告警
- JSON 直接用 Vite 原生 JSON import，无额外构建配置

## 3. 命名空间设计

按功能域拆分，初始 15 个：

| 命名空间 | 覆盖范围 |
|---|---|
| `common` | 通用按钮/标签/状态词（保存、取消、删除、搜索、加载中、成功/失败…），全部页面共享 |
| `dashboard` | 概览页 |
| `providers` | Provider 配置与 key 管理 |
| `governance` | 虚拟 key、团队、客户、预算、限流 |
| `playground` | Playground |
| `logs` | 日志查询与详情 |
| `mcp` | MCP 注册表、会话、工具组、设置 |
| `settings` | 系统设置 |
| `models` | 模型目录、模型限制、自定义定价 |
| `plugins` | 插件管理 |
| `auth` | 登录、会话、认证配置 |
| `setup` | 首次安装向导 |
| `analytics` | 数据分析、告警 |
| `workspaces` | 工作区壳层（侧边栏、Topbar、导航） |
| `errors` | 错误页、404、边界错误 |

规则：
- key 命名 `区块.位置.语义`，如 `providers.keys.deleteConfirm`
- 通用词汇一律收敛到 `common`，保证翻译一致性
- `common` 命名空间在批次 0 完成后**冻结**（批次执行期间只读，避免并行冲突）

## 4. 组件改造模式

```tsx
import { useTranslation } from "react-i18next";

export function DeleteKeyButton() {
  const { t } = useTranslation(["common", "providers"]);
  return (
    <Button variant="destructive" onClick={...}>
      {t("providers:keys.deleteConfirm", { keyName: name })}
    </Button>
  );
}
```

- 插值用 i18next 内置 `{{var}}`；复数用内置复数规则（`_one` / `_other`）
- 数字/日期格式化沿用现有 `Intl.NumberFormat`、date-fns + 时区偏好逻辑，不迁移到 i18next format（避免回归）
- `data-testid` 一律不动（E2E 依赖）

## 5. 语言切换器

- 新组件 `ui/components/ui/languageSwitcher.tsx`，基于现有 `Select`（`ui/components/ui/select.tsx`）封装
- 放置：`ui/components/topbar.tsx` 右侧用户区；`ui/app/workspace/settings` 设置页增加同等能力的入口项
- 切换行为：`i18n.changeLanguage(lng)` + 写 localStorage；语言包未加载时 `changeLanguage` 异步解析后自动重渲染

## 6. 并行执行策略

批次 0（基础设施，串行，单独完成）：
1. 安装 `i18next` + `react-i18next`（精确锁版本，写入 `ui/package.json`）
2. `ui/lib/i18n/` 全部基础设施 + `main.tsx` 接入
3. 语言切换器组件 + Topbar 接入
4. `parity.test.ts` + i18n 初始化单元测试
5. 翻译 `common` 命名空间并冻结
6. 门禁自检：`tsc --noEmit`、`oxlint`、`oxfmt`、`vitest`、`npm run build`

批次 1–8（并行，每批一个独立 agent，按功能域合并 38 个路由区域）：
- 每批独立完成：抽取该域英文文案 → 生成 `en/<ns>.json` 与 `zh-CN/<ns>.json` → 替换组件内字符串 → 自检门禁
- 批次之间只依赖已冻结的 `common`，文件互不重叠，无合并冲突
- 每批完成后主会话汇总验收（parity 测试 + 构建通过 + 抽查译文）

批次划分（按路由目录归组，执行时可按实际文件量微调）：
1. `home` + `dashboard` + `analytics` + `alerting`
2. `providers` + `models` 相关（`model-catalog`、`model-limits`、`custom-pricing`）
3. `governance`（最大的域，含虚拟 key/团队/客户/预算）
4. `logs` + `audit-logs` + `mcp-logs` + `agent-logs`
5. `mcp-*`（registry/sessions/settings/tool-groups/auth-config）+ `agent-gateway`
6. `playground` + `prompt-repo` + `skills` 相关
7. `settings` + `plugins` + `config` + `cluster` + `oauth-grants` + `auth` + `setup`
8. 壳层与杂项：`workspaces` 布局组件、`errors`、`ui/components/`（未被前述批次覆盖的共享组件）

## 7. 测试与验证

| 层 | 内容 |
|---|---|
| `parity.test.ts` | 遍历 `locales/en/*.json` 与 `locales/zh-CN/*.json`，断言键结构完全一致（防漂移，每批新增必须过） |
| i18n 单元测试 | 检测优先级（localStorage > 浏览器 > en）、非法 localStorage 值回退、`changeLanguage` 后 `t()` 输出 |
| 每批门禁 | `tsc --noEmit` + `oxlint` + `oxfmt --check` |
| 整体验收 | `npm run build`；`vitest` 全绿；`make dev` 手动切换语言抽查 |
| E2E | 不受影响（全部 `data-testid` 选择器，已验证 0 处英文文案断言） |

## 8. 依赖与约束

- 新增依赖：`i18next`、`react-i18next`，精确锁版本（无 `^`/`~`），符合项目依赖规则；全量翻译规模下自研等于重造轮子，属"绝对必要"范畴
- React 19 / Vite 8 兼容性：`react-i18next` 当前稳定版支持 React 18+，锁版本前验证 peer deps
- 遵循前端守则：不引入其他新库；`npm run format` + `npm run build` 必须通过；E2E `data-testid` 不变

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| 译文质量 | 全部译文集中在 JSON，用户可全局审校；术语表写入 `common` 注释（provider/虚拟 key 等产品词保留英文） |
| 漏翻/漂移 | parity 测试强制 en/zh-CN 同构；`parseMissingKeyHandler` 开发环境告警 |
| 懒加载闪烁 | `fallbackLng: "en"` + 语言包极小（每命名空间 JSONgzip 后 <10KB），加载近瞬时 |
| 并行批次质量不一 | 统一改造模式文档化进实施计划；每批主会话验收 |
