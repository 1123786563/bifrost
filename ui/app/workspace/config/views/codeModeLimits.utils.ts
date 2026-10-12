import type { MCPCodeModeLimits } from "@/lib/types/config";
import type { TFunction } from "i18next";

// Server-side bounds, mirrored from core/schemas MCPCodeModeLimits.Validate.
const MIN_VALUE_BYTES = 1024;
const MAX_NESTING_DEPTH = 1000;

export const CODE_MODE_LIMIT_FIELDS: { key: keyof MCPCodeModeLimits; labelKey: string; descriptionKey: string; defaultValue: number }[] = [
	{
		key: "max_source_bytes",
		labelKey: "settings:mcp.codeMode.fields.max_source_bytes.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_source_bytes.desc",
		defaultValue: 65536,
	},
	{
		key: "max_steps",
		labelKey: "settings:mcp.codeMode.fields.max_steps.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_steps.desc",
		defaultValue: 1000000,
	},
	{
		key: "max_memory_bytes",
		labelKey: "settings:mcp.codeMode.fields.max_memory_bytes.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_memory_bytes.desc",
		defaultValue: 67108864,
	},
	{
		key: "max_log_bytes",
		labelKey: "settings:mcp.codeMode.fields.max_log_bytes.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_log_bytes.desc",
		defaultValue: 65536,
	},
	{
		key: "max_tool_calls",
		labelKey: "settings:mcp.codeMode.fields.max_tool_calls.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_tool_calls.desc",
		defaultValue: 64,
	},
	{
		key: "max_value_bytes",
		labelKey: "settings:mcp.codeMode.fields.max_value_bytes.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_value_bytes.desc",
		defaultValue: 1048576,
	},
	{
		key: "max_nesting_depth",
		labelKey: "settings:mcp.codeMode.fields.max_nesting_depth.label",
		descriptionKey: "settings:mcp.codeMode.fields.max_nesting_depth.desc",
		defaultValue: 64,
	},
];

// Missing, undefined and 0 all mean "use the default", so they compare equal.
export function codeModeLimitsEqual(a: MCPCodeModeLimits | undefined, b: MCPCodeModeLimits | undefined): boolean {
	return CODE_MODE_LIMIT_FIELDS.every(({ key }) => (a?.[key] ?? 0) === (b?.[key] ?? 0));
}

// Returns 0 for an empty input (the default), the value for a whole number >= 0,
// and undefined for anything else so the caller can keep the previous value.
export function parseCodeModeLimitInput(value: string): number | undefined {
	if (value.trim() === "") return 0;
	if (!/^\d+$/.test(value.trim())) return undefined;
	return Number.parseInt(value, 10);
}

export function validateCodeModeLimits(limits: MCPCodeModeLimits | undefined, t: TFunction): string | null {
	if (!limits) return null;
	for (const { key, labelKey } of CODE_MODE_LIMIT_FIELDS) {
		if ((limits[key] ?? 0) < 0) return t("settings:mcp.codeMode.negative", { label: t(labelKey) });
	}
	const valueBytes = limits.max_value_bytes ?? 0;
	if (valueBytes !== 0 && valueBytes < MIN_VALUE_BYTES) {
		return t("settings:mcp.codeMode.valueMin", { min: MIN_VALUE_BYTES });
	}
	if ((limits.max_nesting_depth ?? 0) > MAX_NESTING_DEPTH) {
		return t("settings:mcp.codeMode.depthMax", { max: MAX_NESTING_DEPTH });
	}
	return null;
}