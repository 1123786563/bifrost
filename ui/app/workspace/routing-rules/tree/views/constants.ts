import i18n from "@/lib/i18n";

// ─── Scope config ──────────────────────────────────────────────────────────

// Labels are getters so they resolve through i18n at access time rather than at
// module load — a language switch is picked up without remounting the tree, and
// consumers that read `SCOPE_CONFIG[s].label` directly need no change.
export const SCOPE_CONFIG = {
	virtual_key: {
		get label() {
			return i18n.t("governance:routingRules.sheet.scopeVirtualKey");
		},
		color: "#7c3aed",
		headerClass: "bg-purple-100 dark:bg-purple-900/30",
	},
	team: {
		get label() {
			return i18n.t("governance:routingRules.sheet.scopeTeam");
		},
		color: "#2563eb",
		headerClass: "bg-blue-100 dark:bg-blue-900/30",
	},
	customer: {
		get label() {
			return i18n.t("governance:routingRules.sheet.scopeCustomer");
		},
		color: "#16a34a",
		headerClass: "bg-green-100 dark:bg-green-900/30",
	},
	global: {
		get label() {
			return i18n.t("governance:routingRules.sheet.scopeGlobal");
		},
		color: "#6b7280",
		headerClass: "bg-gray-100 dark:bg-gray-800/30",
	},
} as const;

export type ScopeKey = keyof typeof SCOPE_CONFIG;

export const SCOPE_ORDER = ["virtual_key", "team", "customer", "global"] as const;

// ─── Layout constants (LR: W = horizontal, H = vertical) ──────────────────

export const SRC_W = 260;
export const SRC_H = 80;
export const COND_W = 310;
export const COND_H = 76;
export const RULE_W = 220;
export const RULE_H = 106;
/** Baseline horizontal spacing intent (Dagre uses ranksep for rank-to-rank gaps). */
export const H_GAP = 280;
/** Baseline vertical spacing intent (Dagre uses nodesep within a rank). */
export const V_GAP = 36;

/** Dagre: minimum horizontal gap between layers (LR ranks / columns). Higher = calmer graph. */
export const DAGRE_RANKSEP = 300;
/** Dagre: minimum vertical gap between nodes sharing a rank. */
export const DAGRE_NODESEP = 52;
/** Dagre: margin around the laid-out bounding box. */
export const DAGRE_MARGIN = 48;

/** Default padding when fitting the graph to the viewport (fraction of viewport). */
export const FIT_VIEW_PADDING = 0.14;