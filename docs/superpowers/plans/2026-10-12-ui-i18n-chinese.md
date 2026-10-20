# Bifrost UI i18n（英文 + 简体中文）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Bifrost Web UI 建立i18n 基础设施，并完成全部 38 个 workspace 路由区域的英文 + 简体中文双语化。

**Architecture:** react-i18next 全局实例 + 自研轻量检测/懒加载 backend。语言包按命名空间拆分 JSON、按需动态加载；8 个互不重叠文件域的翻译批次可并行执行，共享 `common` 命名空间在批次 0 冻结。

**Tech Stack:** `i18next@26.4.4`、`react-i18next@17.0.16`、Vite `import.meta.glob`、vitest（已有）。

**Spec:** `docs/specs/superpowers/2026-10-12-ui-i18n-chinese-design.md`（本计划从 spec 出发；执行者须同时读两份）

## Global Constraints

- 新依赖只有两个，精确锁版本：`i18next@26.4.4`、`react-i18next@17.0.16`（已验证 peer deps 兼容 React 19.2.3 / TS 5.9.2）
- 所有 `data-testid` 一律保留不动；新增交互元素必须加 `data-testid`，命名 `<entity>-<element>-<qualifier>`
- 不翻译后端数据：provider 名、模型名、日志内容、API 错误信息、MCP 工具描述
- 企业版目录（`ui/app/enterprise`）在本 checkout 不存在，无需处理；若后续出现，跳过其下文件
- UI 命令一律在 `ui/` 目录执行；门禁 = `npm run typecheck` + `npm run lint` + `npm run format:check` + `npx vitest run`
- 不要在批次任务里跑 `npm run build`（会拷贝产物到 `transports/bifrost-http/ui`，只在最终验收任务跑）
- Git 提交用 conventional 风格：`feat(ui): ...`；每个任务结束即提交
- 工作目录是 `/Users/wuyongjun/trea/bifrost`；`make dev` 若需要启动，用 `PATH="$HOME/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.0.darwin-arm64/bin:$PATH" BIFROST_SETUP_TOKEN=$(cat /tmp/bifrost-setup-token.txt) make dev PORT=8080`

---

## 批次 0：基础设施（串行，主会话执行）

### Task 0.1: 安装依赖

**Files:**
- Modify: `ui/package.json`、`ui/package-lock.json`

**Interfaces:**
- Produces: `i18next` 与 `react-i18next` 可从 `ui/` 内任何文件 import

- [ ] **Step 1: 安装并锁版本**

```bash
cd ui && npm install --save-exact i18next@26.4.4 react-i18next@17.0.16
```

- [ ] **Step 2: 验证 package.json 中是精确版本（无 `^`/`~`）**

```bash
grep -E '"(i18next|react-i18next)"' ui/package.json
```

Expected: `"i18next": "26.4.4"` 和 `"react-i18next": "17.0.16"`

- [ ] **Step 3: Commit**

```bash
git add ui/package.json ui/package-lock.json
git commit -m "feat(ui): add i18next and react-i18next dependencies"
```

---

### Task 0.2: i18n 基础设施（TDD：先写检测与 parity 测试）

**Files:**
- Create: `ui/lib/i18n/detection.ts`
- Create: `ui/lib/i18n/detection.test.ts`
- Create: `ui/lib/i18n/namespaces.ts`
- Create: `ui/lib/i18n/lazyResources.ts`
- Create: `ui/lib/i18n/index.ts`
- Create: `ui/lib/i18n/parity.test.ts`
- Create: `ui/lib/i18n/locales/en/common.json`（先放 `{}` 占位，Task 0.4 填充）
- Create: `ui/lib/i18n/locales/zh-CN/common.json`（同上）

**Interfaces:**
- Produces（后续所有任务依赖，签名精确如下）:
  - `ui/lib/i18n/detection.ts`：`type Language = "en" | "zh-CN"`；`SUPPORTED_LANGUAGES: readonly Language[]`；`detectInitialLanguage(): Language`；`changeLanguage(language: Language): void`
  - `ui/lib/i18n/index.ts`：默认导出已初始化的 i18next 实例；重导出 `SUPPORTED_LANGUAGES`、`LANGUAGE_DISPLAY_NAMES: Record<Language, string>`、`changeLanguage`
  - 命名空间：15 个，名称见 Task 0.2 Step 3 的 `namespaces.ts`（同样新建）

- [ ] **Step 1: 写失败的检测测试**

创建 `ui/lib/i18n/detection.test.ts`：

```typescript
import { describe, expect, it } from "vitest";
import { detectLanguage } from "./detection";

describe("detectLanguage", () => {
	it("stored preference wins over browser languages", () => {
		expect(detectLanguage("zh-CN", ["en-US"])).toBe("zh-CN");
		expect(detectLanguage("en", ["zh-CN"])).toBe("en");
	});

	it("detects zh from any zh-* browser language", () => {
		expect(detectLanguage(null, ["zh-CN", "en"])).toBe("zh-CN");
		expect(detectLanguage(null, ["zh-TW"])).toBe("zh-CN");
		expect(detectLanguage(undefined, ["en-US", "zh"])).toBe("en");
	});

	it("falls back to en when nothing matches", () => {
		expect(detectLanguage(null, ["fr-FR", "ja-JP"])).toBe("en");
		expect(detectLanguage(null, [])).toBe("en");
		expect(detectLanguage(undefined, undefined)).toBe("en");
	});

	it("treats invalid stored values as unset", () => {
		expect(detectLanguage("fr", ["en-US"])).toBe("en");
		expect(detectLanguage("zh", ["en-US"])).toBe("en");
		expect(detectLanguage("", [])).toBe("en");
	});
});
```

- [ ] **Step 2: 运行确认失败**

```bash
cd ui && npx vitest run lib/i18n/detection.test.ts
```

Expected: FAIL，`Cannot find module './detection'`

- [ ] **Step 3: 实现基础设施**

创建 `ui/lib/i18n/detection.ts`：

```typescript
export const STORAGE_KEY = "bifrost-language";

export const SUPPORTED_LANGUAGES = ["en", "zh-CN"] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];
export const DEFAULT_LANGUAGE: Language = "en";

export function isSupportedLanguage(value: string | null | undefined): value is Language {
	return value === "en" || value === "zh-CN";
}

/**
 * Pure decision logic: stored preference wins, then the first browser language
 * whose prefix matches a supported locale, then the default. Kept DOM-free so it
 * is unit-testable in a plain node vitest environment.
 */
export function detectLanguage(
	stored: string | null | undefined,
	browserLanguages: readonly string[] | undefined,
): Language {
	if (isSupportedLanguage(stored)) return stored;
	for (const tag of browserLanguages ?? []) {
		const lower = tag.toLowerCase();
		if (lower.startsWith("zh")) return "zh-CN";
		if (lower.startsWith("en")) return "en";
	}
	return DEFAULT_LANGUAGE;
}

export function readStoredLanguage(): string | null {
	try {
		return window.localStorage.getItem(STORAGE_KEY);
	} catch {
		return null;
	}
}

export function persistLanguage(language: Language): void {
	try {
		window.localStorage.setItem(STORAGE_KEY, language);
	} catch {
		// Storage disabled (private mode): preference just won't survive reloads.
	}
}

export function detectInitialLanguage(): Language {
	let browser: readonly string[] = [];
	try {
		browser = navigator.languages ?? [navigator.language];
	} catch {
		// Non-browser environment: fall through to default.
	}
	return detectLanguage(readStoredLanguage(), browser);
}
```

创建 `ui/lib/i18n/namespaces.ts`：

```typescript
/** Every locale namespace. A batch translation task adds files per namespace; the
 * lazy glob loader and parity test pick new files up automatically. */
export const NAMESPACES = [
	"common",
	"workspaces",
	"setup",
	"auth",
	"dashboard",
	"providers",
	"governance",
	"logs",
	"playground",
	"mcp",
	"models",
	"plugins",
	"settings",
	"analytics",
	"errors",
] as const;
export type Namespace = (typeof NAMESPACES)[number];
```

创建 `ui/lib/i18n/lazyResources.ts`：

```typescript
import type { Language } from "./detection";
import type { Namespace } from "./namespaces";

// Vite glob import: no per-file registry to maintain. Locale files added by later
// batches are picked up automatically at build time.
const modules = import.meta.glob<{ default: object }>("./locales/*/*.json");

export async function loadNamespace(language: Language, namespace: Namespace): Promise<object> {
	const load = modules[`./locales/${language}/${namespace}.json`];
	if (!load) throw new Error(`Missing locale file: ${language}/${namespace}.json`);
	const mod = await load();
	return mod.default;
}
```

创建 `ui/lib/i18n/index.ts`：

```typescript
import i18next, { type BackendModule } from "i18next";
import { initReactI18next } from "react-i18next";

import { detectInitialLanguage, persistLanguage, type Language } from "./detection";
import { loadNamespace } from "./lazyResources";
import type { Namespace } from "./namespaces";

export { SUPPORTED_LANGUAGES } from "./detection";
export type { Language } from "./detection";

/** Language autonyms — always shown in their own language, never translated. */
export const LANGUAGE_DISPLAY_NAMES: Record<Language, string> = {
	en: "English",
	"zh-CN": "中文",
};

const lazyBackend: BackendModule = {
	type: "backend",
	init() {},
	read(language, namespace, callback) {
		loadNamespace(language as Language, namespace as Namespace)
			.then((resources) => callback(null, resources))
			.catch((error) => callback(error, null));
	},
};

void i18next.use(initReactI18next).use(lazyBackend).init({
	lng: detectInitialLanguage(),
	fallbackLng: "en",
	defaultNS: "common",
	// i18next resolves a missing key through fallbackLng, reading en/<ns>.json
	// through the same lazy backend — the "load both, then render" behavior the
	// spec requires, with no extra wiring.
	ns: ["common"],
	interpolation: { escapeValue: false },
	react: { useSuspense: false },
	missingKeyHandler: (_lngs, ns, key) => {
		if (import.meta.env.DEV) console.warn(`[i18n] missing key "${ns}:${key}"`);
	},
});

export function changeLanguage(language: Language): void {
	persistLanguage(language);
	void i18next.changeLanguage(language);
}

export default i18next;
```

创建两个占位语言包 `ui/lib/i18n/locales/en/common.json` 和 `ui/lib/i18n/locales/zh-CN/common.json`，内容均为：

```json
{}
```

- [ ] **Step 4: 写 parity 测试**

创建 `ui/lib/i18n/parity.test.ts`：

```typescript
import { describe, expect, it } from "vitest";

const localeModules = import.meta.glob<{ default: Record<string, unknown> }>("./locales/*/*.json", {
	eager: true,
});

function flattenKeys(value: unknown, prefix = ""): string[] {
	if (value === null || typeof value !== "object") return prefix ? [prefix] : [];
	return Object.entries(value).flatMap(([key, child]) =>
		flattenKeys(child, prefix ? `${prefix}.${key}` : key),
	);
}

const files = Object.entries(localeModules);
const languages = new Set(files.map(([path]) => path.split("/")[2]));
const namespaces = new Set(files.map(([path]) => path.split("/")[3].replace(/\.json$/, "")));

describe("locale parity", () => {
	it("every namespace exists in every language", () => {
		for (const ns of namespaces) {
			for (const lng of languages) {
				const file = files.find(([path]) => path === `./locales/${lng}/${ns}.json`);
				expect(file, `missing ${lng}/${ns}.json`).toBeDefined();
			}
		}
	});

	it("zh-CN key structure matches en exactly, for every namespace", () => {
		for (const ns of namespaces) {
			const en = localeModules[`./locales/en/${ns}.json`]?.default;
			const zh = localeModules[`./locales/zh-CN/${ns}.json`]?.default;
			expect(en, `missing en/${ns}.json`).toBeDefined();
			expect(zh, `missing zh-CN/${ns}.json`).toBeDefined();
			expect(flattenKeys(zh).sort(), `key drift in ${ns}`).toEqual(flattenKeys(en).sort());
		}
	});
});
```

- [ ] **Step 5: 运行全部 i18n 测试确认通过**

```bash
cd ui && npx vitest run lib/i18n/
```

Expected: detection.test.ts 4 个用例 PASS，parity.test.ts 2 个用例 PASS

- [ ] **Step 6: Commit**

```bash
git add ui/lib/i18n/
git commit -m "feat(ui): i18n infrastructure — detection, lazy locale backend, parity test"
```

---

### Task 0.3: 应用入口接入

**Files:**
- Modify: `ui/app/main.tsx`（import 区，第 8 行 `import "@/app/globals.css";` 之后）

**Interfaces:**
- Consumes: `ui/lib/i18n`（Task 0.2，副作用导入初始化实例）

- [ ] **Step 1: 在 `main.tsx` 的 `import "@/app/globals.css";` 之后加一行**

```typescript
// Initialize i18n before the router renders so first paint already has translations.
import "@/lib/i18n";
```

- [ ] **Step 2: 门禁自检**

```bash
cd ui && npm run typecheck && npm run lint
```

Expected: 均无错误

- [ ] **Step 3: Commit**

```bash
git add ui/app/main.tsx
git commit -m "feat(ui): initialize i18n before router render"
```

---

### Task 0.4: `common` 命名空间翻译并冻结

**Files:**
- Modify: `ui/lib/i18n/locales/en/common.json`
- Modify: `ui/lib/i18n/locales/zh-CN/common.json`

**Interfaces:**
- Produces: 批次 1–8 全部依赖的通用词条。**此任务提交后 `common.json` 冻结**——后续批次只能使用下列 key，不得新增/修改/删除。通用词必须从这里取，保证全站翻译一致。

- [ ] **Step 1: 填入 `en/common.json`**

```json
{
	"language": "Language",
	"actions": { "save": "Save", "cancel": "Cancel", "delete": "Delete", "edit": "Edit", "create": "Create", "add": "Add", "remove": "Remove", "search": "Search", "refresh": "Refresh", "retry": "Retry", "close": "Close", "confirm": "Confirm", "copy": "Copy", "copied": "Copied", "download": "Download", "upload": "Upload", "export": "Export", "import": "Import", "back": "Back", "next": "Next", "previous": "Previous", "done": "Done", "apply": "Apply", "reset": "Reset", "submit": "Submit", "update": "Update", "view": "View", "test": "Test", "expand": "Expand", "collapse": "Collapse", "loadMore": "Load more", "clear": "Clear", "enable": "Enable", "disable": "Disable" },
	"state": { "loading": "Loading…", "saving": "Saving…", "success": "Success", "error": "Error", "failed": "Failed", "active": "Active", "inactive": "Inactive", "enabled": "Enabled", "disabled": "Disabled", "pending": "Pending", "running": "Running", "unknown": "Unknown", "none": "None", "all": "All", "empty": "Nothing here yet" },
	"fields": { "name": "Name", "description": "Description", "type": "Type", "status": "Status", "actions": "Actions", "details": "Details", "createdAt": "Created at", "updatedAt": "Updated at", "optional": "Optional", "required": "Required" },
	"messages": { "deleteConfirm": "Are you sure you want to delete this?", "unsavedChanges": "You have unsaved changes. Leave anyway?", "notFound": "Not found", "copySuccess": "Copied to clipboard", "operationFailed": "Operation failed. Please try again." }
}
```

- [ ] **Step 2: 填入 `zh-CN/common.json`（键结构必须与 en 完全一致）**

```json
{
	"language": "语言",
	"actions": { "save": "保存", "cancel": "取消", "delete": "删除", "edit": "编辑", "create": "创建", "add": "添加", "remove": "移除", "search": "搜索", "refresh": "刷新", "retry": "重试", "close": "关闭", "confirm": "确认", "copy": "复制", "copied": "已复制", "download": "下载", "upload": "上传", "export": "导出", "import": "导入", "back": "返回", "next": "下一步", "previous": "上一步", "done": "完成", "apply": "应用", "reset": "重置", "submit": "提交", "update": "更新", "view": "查看", "test": "测试", "expand": "展开", "collapse": "收起", "loadMore": "加载更多", "clear": "清除", "enable": "启用", "disable": "禁用" },
	"state": { "loading": "加载中…", "saving": "保存中…", "success": "成功", "error": "错误", "failed": "失败", "active": "活跃", "inactive": "未激活", "enabled": "已启用", "disabled": "已禁用", "pending": "待处理", "running": "运行中", "unknown": "未知", "none": "无", "all": "全部", "empty": "暂无内容" },
	"fields": { "name": "名称", "description": "描述", "type": "类型", "status": "状态", "actions": "操作", "details": "详情", "createdAt": "创建时间", "updatedAt": "更新时间", "optional": "可选", "required": "必填" },
	"messages": { "deleteConfirm": "确定要删除吗？", "unsavedChanges": "有未保存的更改，确定离开吗？", "notFound": "未找到", "copySuccess": "已复制到剪贴板", "operationFailed": "操作失败，请重试。" }
}
```

- [ ] **Step 3: 门禁自检（重点 parity）**

```bash
cd ui && npx vitest run lib/i18n/parity.test.ts && npm run format && npm run typecheck
```

Expected: parity PASS

- [ ] **Step 4: Commit**

```bash
git add ui/lib/i18n/locales/
git commit -m "feat(ui): translate common namespace and freeze it"
```

---

### Task 0.5: 语言切换器 + Topbar 接入

**Files:**
- Create: `ui/components/ui/languageSwitcher.tsx`
- Modify: `ui/components/topbar.tsx`（`<ThemeToggle />` 处，约 192 行）

**Interfaces:**
- Consumes: `ui/lib/i18n` 的 `SUPPORTED_LANGUAGES`、`LANGUAGE_DISPLAY_NAMES`、`changeLanguage`、`type Language`（Task 0.2/0.4）

- [ ] **Step 1: 创建切换器（仿 `ui/components/themeToggle.tsx` 的 DropdownMenu 模式）**

创建 `ui/components/ui/languageSwitcher.tsx`：

```tsx
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { changeLanguage, LANGUAGE_DISPLAY_NAMES, SUPPORTED_LANGUAGES } from "@/lib/i18n";
import { Check, Languages } from "lucide-react";
import { useTranslation } from "react-i18next";

/**
 * Language autonyms as bare dropdown items, embeddable in a larger menu like ThemeToggleItems.
 */
export function LanguageSwitcherItems() {
	const { i18n } = useTranslation("common");

	return (
		<>
			{SUPPORTED_LANGUAGES.map((language) => (
				<DropdownMenuItem
					key={language}
					data-testid={`language-switcher-option-${language}`}
					onClick={() => changeLanguage(language)}
					className="cursor-pointer"
				>
					<span className="flex-1">{LANGUAGE_DISPLAY_NAMES[language]}</span>
					{i18n.language === language && <Check className="text-muted-foreground size-3.5" strokeWidth={2.5} />}
				</DropdownMenuItem>
			))}
		</>
	);
}

export function LanguageSwitcher() {
	const { t } = useTranslation("common");

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					data-testid="language-switcher-trigger"
					// Same icon-box sizing as the theme toggle so topbar icons stay evenly spaced.
					className="text-muted-foreground hover:bg-accent hover:text-accent-foreground data-[state=open]:bg-card data-[state=open]:text-accent-foreground size-8 border-0 ring-offset-0 outline-none select-none focus-visible:ring-0 data-[state=open]:border"
				>
					<Languages className="size-4" strokeWidth={2} />
					<span className="sr-only">{t("language")}</span>
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" sideOffset={2}>
				<LanguageSwitcherItems />
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
```

- [ ] **Step 2: Topbar 接入（`<ThemeToggle />` 旁）**

`ui/components/topbar.tsx`：import 区加 `import { LanguageSwitcher } from "@/components/ui/languageSwitcher";`，JSX 中 `<ThemeToggle />` 改为：

```tsx
<LanguageSwitcher />
<ThemeToggle />
```

- [ ] **Step 3: 门禁自检**

```bash
cd ui && npm run typecheck && npm run lint && npm run format && npx vitest run
```

Expected: 全部通过

- [ ] **Step 4: 手动验证**

启动 dev 服务器（见 Global Constraints），打开 http://localhost:8080：Topbar 出现语言图标；点击选「中文」，界面语言变为中文并刷新后保持（localStorage）。浏览器语言为英文时首次访问显示英文。

- [ ] **Step 5: Commit**

```bash
git add ui/components/ui/languageSwitcher.tsx ui/components/topbar.tsx
git commit -m "feat(ui): language switcher in topbar"
```

---

### Task 0.6: E2E 覆盖语言切换

**Files:**
- Create: `tests/e2e/features/language/language.spec.ts`

**Interfaces:**
- Consumes: `data-testid="language-switcher-trigger"` / `"language-switcher-option-zh-CN"`（Task 0.5）；Topbar 挂载页面（默认落地路由）

- [ ] **Step 1: 写 spec（仿 `tests/e2e/features/dashboard/dashboard.spec.ts` 的 fixture 导入方式）**

```typescript
import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Language switcher', () => {
  test('switches the UI to Chinese and persists after reload', async ({ page }) => {
    await page.goto('/')
    await page.getByTestId('language-switcher-trigger').click()
    await page.getByTestId('language-switcher-option-zh-CN').click()

    // The trigger's sr-only label is translated from common:language.
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('语言')

    await page.reload()
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('语言')
  })

  test('defaults to English when no preference is stored', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('language-switcher-trigger')).toContainText('Language')
  })
})
```

- [ ] **Step 2: 运行**

```bash
make run-e2e FLOW=language
```

Expected: 2 个用例 PASS（需要 dev 服务器在跑，见 Global Constraints；`language` 对应 features 下的目录名）

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/features/language/
git commit -m "test(e2e): cover language switch and persistence"
```

---

## 批次 1–8：并行翻译（每批一个独立 agent，互不触碰对方文件）

### 全批次共同契约（每个批次 agent 都必须完整遵守，下文各任务不重复）

**允许修改的文件：**
- `ui/lib/i18n/locales/en/<本批命名空间>.json` 与 `ui/lib/i18n/locales/zh-CN/<本批命名空间>.json`（新建）
- 本批分配的 `ui/app/workspace/<路由目录>/` 下所有文件

**严禁修改：**
- `ui/lib/i18n/` 下任何已有文件（`common.json` 已冻结；`namespaces.ts`/`index.ts`/`lazyResources.ts` 不需要动——新建 JSON 会被 glob 自动拾取）
- `ui/components/` 下的共享组件（其中的英文文案归批次 8，本批遇到就跳过并在报告中列出）
- 任何 `data-testid`、任何后端数据字符串（provider/模型名、日志内容、API 错误信息、工具描述）
- 其他批次的路由目录

**改造模式（唯一合法写法）：**

```tsx
import { useTranslation } from "react-i18next";

export function CreateTeamDialog() {
	// 只订阅本页用到的命名空间；common 永远第一个
	const { t } = useTranslation(["common", "governance"]);
	return (
		<>
			<h2>{t("governance:teams.createTitle")}</h2>
			<Button>{t("common:actions.save")}</Button>
			{/* 插值 */}
			<span>{t("governance:teams.memberCount", { count: members.length })}</span>
			{/* 常量数组里的文案：常量存 key，渲染处翻译 */}
			<SelectItem>{t(item.labelKey)}</SelectItem>
		</>
	);
}
```

**翻译规则：**
1. 通用词（保存/取消/删除/搜索/状态/名称等）一律用 `common:` key（词条清单见 Task 0.4），不得在本批重复定义
2. 产品术语保留英文：Bifrost、Provider、Virtual Key（可译「虚拟密钥」？——**不，统一保留 Virtual Key**）、MCP、Playground、Token（计费语境保留）
3. 插值变量原样保留：`"logs.rangeLabel": "Showing {{from}} – {{to}}"` → `"显示 {{from}} – {{to}}"`
4. 复数：出现数量文案时用 i18next 复数后缀 `_one`/`_other`，中英文都要（中文两个后缀内容相同）
5. en JSON 的值就是现英文文案（含原有大小写与标点）；zh 译文用简体、全角标点、中英之间加空格（如「已创建 3 个团队」）
6. Toast/报错短语以「。」结尾与否跟 en 原文对齐

**每个批次的执行步骤（checkbox）：**

- [ ] **Step 1: 盘点** — `grep -rn '"[A-Z][a-z]' ui/app/workspace/<本批目录>/ --include="*.tsx"` 列出所有界面文案，同时找出 `ui/lib/constants/` 中被本批页面渲染的文案常量
- [ ] **Step 2: 建 JSON** — 创建 `en/<ns>.json` + `zh-CN/<ns>.json`，按 `区块.位置.语义` 组织 key
- [ ] **Step 3: 迁移组件** — 按上述模式替换；路由 `layout.tsx` 里的页面标题/侧边栏标签同样处理
- [ ] **Step 4: 门禁** — `cd ui && npm run typecheck && npm run lint && npm run format && npx vitest run lib/i18n/parity.test.ts`，全绿才继续
- [ ] **Step 5: Commit** — `git add <本批文件> && git commit -m "feat(ui): i18n batch N — <ns 列表>"`
- [ ] **Step 6: 批次报告** — 列出：迁移文件数、key 数、跳过的共享组件清单（含文件:行）、不确定的译法

### Task 1: 批次 1 — analytics 域

**Files:** `ui/app/workspace/home/`、`dashboard/`、`analytics/`、`alerting/` → 命名空间 `dashboard`、`analytics`

### Task 2: 批次 2 — providers/models 域

**Files:** `ui/app/workspace/providers/`、`model-catalog/`、`model-limits/`、`custom-pricing/` → 命名空间 `providers`、`models`

### Task 3: 批次 3 — governance 域（最大）

**Files:** `ui/app/workspace/governance/`、`virtual-keys/`、`rbac/`、`routing-rules/`、`circuit-breaker/`、`complexity-router/`、`adaptive-routing/`、`edge-control/`、`guardrails/`、`webhooks/` → 命名空间 `governance`

### Task 4: 批次 4 — logs 域

**Files:** `ui/app/workspace/logs/`、`audit-logs/`、`mcp-logs/`、`agent-logs/`、`observability/` → 命名空间 `logs`

### Task 5: 批次 5 — MCP/agent 域

**Files:** `ui/app/workspace/mcp-registry/`、`mcp-sessions/`、`mcp-settings/`、`mcp-tool-groups/`、`mcp-auth-config/`、`agent-gateway/`、`virtual-mcps/` → 命名空间 `mcp`

### Task 6: 批次 6 — prompts/skills/docs 域

**Files:** `ui/app/workspace/prompt-repo/`、`skills-repo/`、`docs/` → 命名空间 `playground`（本 checkout 无 Playground 路由，该命名空间承载 prompts/skills/docs 文案，保持 15 命名空间与 spec 一致）

### Task 7: 批次 7 — config/plugins/auth 域

**Files:** `ui/app/workspace/config/`、`cluster/`、`plugins/`、`oauth-grants/`、`scim/` → 命名空间 `settings`（承载 config/cluster 页面文案）、`plugins`、`auth`

### Task 8: 批次 8 — 壳层与共享组件（**必须在批次 1–7 全部合并后执行**）

**Files:** `ui/components/`（topbar、sidebar 及所有共享组件中的文案）、`ui/app/workspace/layout.tsx`、`ui/app/__error.tsx`、`ui/app/__notFound.tsx`、`ui/app/__updating.tsx`、setup/登录流程页面（若存在且未被批次 7 覆盖）→ 命名空间 `workspaces`、`setup`、`errors`

额外规则：先 `grep -rn` 批次 1–7 报告中列出的共享组件清单，逐一处理；侧边栏/Topbar 导航标签从路由配置读的地方，改为渲染处 `t()`。spec §5 要求的「设置页语言入口」也归本批：本 checkout 无独立 settings 页，若 `config` 页或账户菜单存在合适的展示面，将已有的 `LanguageSwitcherItems`（Task 0.5 已做成可嵌入组件）嵌入其中；若没有合适位置，在报告中说明后跳过（Topbar 入口已覆盖切换能力）。

---

## 最终验收（串行，主会话执行）

### Task 9: 全量验收

- [ ] **Step 1: 全量门禁**

```bash
cd ui && npm run typecheck && npm run lint && npm run format:check && npx vitest run && npm run build
```

- [ ] **Step 2: 手动验收** — `make dev`（命令见 Global Constraints）后逐页抽查中英文切换：Dashboard、Providers、Governance、Logs、MCP、Playground、Settings；确认后端数据（provider/模型名、日志内容）未被翻译；确认 setup 页（无痕窗口）在浏览器中文环境下显示中文

- [ ] **Step 3: E2E 回归** — `make run-e2e FLOW=language`，并抽样跑 `make run-e2e FLOW=dashboard` 确认既有测试不受影响

- [ ] **Step 4: 收尾提交** — 若有微调，`git commit`；在批次报告中汇总全部 key 总数与覆盖率（`grep -c` 各 JSON）

---

## 与 spec 的偏差记录

1. **未启用 i18next 严格 key 类型（`CustomTypeOptions.resources`）**：spec 第 2.1 节提到 TS 类型安全。迁移期 300+ 文件含大量常量驱动的动态 key，严格类型会大量阻塞并行批次；改用「parity 测试 + DEV 环境 missing-key 告警 + 人工抽查」实现同等防漂移保障。批次全部完成后可另开任务启用严格类型。
2. **`useSuspense: false`**：语言包加载期间组件短暂显示英文（en 为 fallback 源且与 zh 并行加载），不引入 Suspense 边界包裹 38 个路由的复杂度。spec 第 9 节已把此列为可接受风险。
3. **设置页语言入口**：spec §5 的设置页（`ui/app/workspace/settings/`）在本 checkout 不存在，入口项归入批次 8 按实际情况处理（Topbar 入口已覆盖核心能力）。
