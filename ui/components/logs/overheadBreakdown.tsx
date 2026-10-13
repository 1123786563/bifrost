import BlockHeader from "@/app/workspace/logs/views/blockHeader";
import type { OverheadBucket } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ChevronDown } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";

// formatMicros renders a microsecond overhead value, promoting to ms once it is
// large enough that microseconds would just be noise.
function formatMicros(us: number): string {
	if (us >= 1000) return `${(us / 1000).toFixed(2)} ms`;
	return `${us.toFixed(us < 10 ? 1 : 0)} µs`;
}

// Top-level overhead categories shown in the stacked bar + legend. Raw backend span
// names are grouped into a handful of user-facing categories: Serialization (JSON
// parse/encode), Conversion (API schema translation), Plugins, Middleware (auth/access),
// Key selection, Processing (internal request pipeline), Networking
// (client<->gateway<->provider handling), Client delivery (SSE egress to the client), and Miscellaneous
// (small glue on no dedicated span plus the residual goroutine-hop latency between phases). "{t("common:actions.viewDetails")}" drills into the member
// spans inside each grouped category with their friendly labels. See OVERHEAD_LABELS /
// OVERHEAD_BUCKET_CATEGORY / overheadCategoryKey for the mapping.
type OverheadCategory = {
	key: string;
	labelKey: string;
	colorClass: string;
	totalUs: number;
	members: OverheadBucket[];
};

// The four serialization phases collapsed into the "Serialization" category. Their
// per-phase labels below are used for the drill-down rows.
const OVERHEAD_SERIALIZATION_PHASES = new Set(["request-unmarshal", "request-marshal", "response-parse", "response-marshal"]);

// Top-level categories shown in the stacked bar + legend. Each has a distinct colour.
const OVERHEAD_CATEGORY_META: Record<string, { labelKey: string; colorClass: string }> = {
	serialization: { labelKey: "workspaces:overhead.serialization", colorClass: "bg-indigo-500/70" },
	conversion: { labelKey: "workspaces:overhead.conversion", colorClass: "bg-fuchsia-500/70" },
	plugins: { labelKey: "workspaces:overhead.plugins", colorClass: "bg-blue-500/70" },
	middleware: { labelKey: "workspaces:overhead.middleware", colorClass: "bg-cyan-500/70" },
	routing: { labelKey: "workspaces:overhead.routing", colorClass: "bg-amber-500/70" },
	processing: { labelKey: "workspaces:overhead.processing", colorClass: "bg-teal-500/70" },
	protocol_sdk: { labelKey: "workspaces:overhead.protocol_sdk", colorClass: "bg-orange-500/70" },
	networking: { labelKey: "workspaces:overhead.networking", colorClass: "bg-emerald-500/70" },
	streaming: { labelKey: "workspaces:overhead.streaming", colorClass: "bg-red-500/70" },
	miscellaneous: { labelKey: "workspaces:overhead.miscellaneous", colorClass: "bg-slate-500/70" },
	other: { labelKey: "workspaces:overhead.other", colorClass: "bg-muted-foreground/50" },
};

// Friendly drill-down labels for each raw bucket name (the technical span names the
// backend emits). Members without an entry fall back to the name with any "plugin."
// prefix stripped.
const OVERHEAD_LABEL_KEYS: Record<string, string> = {
	// Serialization (JSON parse / encode)
	"request-unmarshal": "workspaces:overheadMembers.request_unmarshal",
	"request-marshal": "workspaces:overheadMembers.request_marshal",
	"response-parse": "workspaces:overheadMembers.response_parse",
	"response-marshal": "workspaces:overheadMembers.response_marshal",
	// Conversion (API schema translation)
	convertor: "workspaces:overheadMembers.convertor",
	"convertor.stream-in": "workspaces:overheadMembers.convertor_stream_in",
	"convertor.stream-out": "workspaces:overheadMembers.convertor_stream_out",
	// Middleware (auth / access control)
	"middleware.apikeys": "workspaces:overheadMembers.middleware_apikeys",
	"middleware.scim": "workspaces:overheadMembers.middleware_scim",
	"middleware.auth": "workspaces:overheadMembers.middleware_auth",
	// Routing
	"key-pool": "workspaces:overheadMembers.key_pool",
	"key.selection": "workspaces:overheadMembers.key_selection",
	// Processing (internal request pipeline)
	"handle-setup": "workspaces:overheadMembers.handle_setup",
	"pipeline-pre": "workspaces:overheadMembers.pipeline_pre",
	"pipeline-post": "workspaces:overheadMembers.pipeline_post",
	"worker-setup": "workspaces:overheadMembers.worker_setup",
	"worker-handoff": "workspaces:overheadMembers.worker_handoff",
	"queue-wait": "workspaces:overheadMembers.queue_wait",
	"attribute-population": "workspaces:overheadMembers.attribute_population",
	miscellaneous: "workspaces:overheadMembers.miscellaneous",
	// Networking (client<->gateway<->provider handling)
	"provider-internal": "workspaces:overheadMembers.provider_internal",
	"transport-context": "workspaces:overheadMembers.transport_context",
	"transport-response-headers": "workspaces:overheadMembers.transport_response_headers",
	"response-finalize": "workspaces:overheadMembers.response_finalize",
	"request-sign": "workspaces:overheadMembers.request_sign",
	"credentials-fetch": "workspaces:overheadMembers.credentials_fetch",
	// Streaming relay
	"stream-backpressure": "workspaces:overheadMembers.stream_backpressure",
	"stream-client-write": "workspaces:overheadMembers.stream_client_write",
	scheduling: "workspaces:overheadMembers.scheduling",
	// A2A agent gateway
	"a2a.normalize": "workspaces:overheadMembers.a2a_normalize",
	"a2a.auth": "workspaces:overheadMembers.a2a_auth",
	"a2a.prepare": "workspaces:overheadMembers.a2a_prepare",
	"a2a.encode": "workspaces:overheadMembers.a2a_encode",
	"a2a.dispatch": "workspaces:overheadMembers.a2a_dispatch",
	"a2a.push.db.authenticate": "workspaces:overheadMembers.a2a_push_db_authenticate",
	"a2a.push.db.bind": "workspaces:overheadMembers.a2a_push_db_bind",
	"a2a.push.db.enqueue": "workspaces:overheadMembers.a2a_push_db_enqueue",
	"a2a.push.db.config": "workspaces:overheadMembers.a2a_push_db_config",
	"a2a.push.db.outcome": "workspaces:overheadMembers.a2a_push_db_outcome",
	"a2a.push.db.local": "workspaces:overheadMembers.a2a_push_db_local",
	"a2a.push.db.save": "workspaces:overheadMembers.a2a_push_db_save",
	"a2a.push.db.delete": "workspaces:overheadMembers.a2a_push_db_delete",
	"a2a.push.db.list-due": "workspaces:overheadMembers.a2a_push_db_list_due",
	"a2a.push.db.prune": "workspaces:overheadMembers.a2a_push_db_prune",
};

// Category assignment for buckets that aren't matched by a prefix rule below. Every
// backend bucket name should be either matched by a prefix rule (serialization phases,
// middleware.*, convertor*, plugin.*) or listed here — otherwise it lands in "Other",
// which is the signal that a new bucket needs a home.
const OVERHEAD_BUCKET_CATEGORY: Record<string, string> = {
	"key-pool": "routing",
	"key.selection": "routing",
	"handle-setup": "processing",
	"pipeline-pre": "processing",
	"pipeline-post": "processing",
	"worker-setup": "processing",
	"worker-handoff": "processing",
	"queue-wait": "processing",
	"attribute-population": "processing",
	miscellaneous: "miscellaneous",
	"provider-internal": "networking",
	"transport-context": "networking",
	"transport-response-headers": "networking",
	"response-finalize": "networking",
	"request-sign": "networking",
	"credentials-fetch": "networking",
	"stream-backpressure": "streaming",
	"stream-client-write": "streaming",
	scheduling: "miscellaneous",
	"a2a.normalize": "serialization",
	"a2a.auth": "middleware",
	"a2a.prepare": "processing",
	"a2a.encode": "serialization",
	"a2a.dispatch": "protocol_sdk",
	"a2a.push.db.authenticate": "processing",
	"a2a.push.db.bind": "processing",
	"a2a.push.db.enqueue": "processing",
	"a2a.push.db.config": "processing",
	"a2a.push.db.outcome": "processing",
	"a2a.push.db.local": "processing",
	"a2a.push.db.save": "processing",
	"a2a.push.db.delete": "processing",
	"a2a.push.db.list-due": "processing",
	"a2a.push.db.prune": "processing",
};

// Raw backend spans that split one user-facing step into internals a reader doesn't care
// about are folded into a single member. key-pool (the pool lookup) + key.selection (the
// actual pick) are both "choosing the API key", so they collapse into "Key selection".
const OVERHEAD_MEMBER_MERGE: Record<string, string> = {
	"key-pool": "key.selection",
};
function mergedBucketName(name: string): string {
	return OVERHEAD_MEMBER_MERGE[name] ?? name;
}

function overheadCategoryKey(b: OverheadBucket): string {
	if (OVERHEAD_SERIALIZATION_PHASES.has(b.name)) {
		return "serialization";
	}
	if (b.name.startsWith("middleware.")) {
		return "middleware";
	}
	// The bare "convertor" phase and the per-chunk streaming variants (convertor.stream-in
	// / .stream-out) all fold into the single Conversion category.
	if (b.name === "convertor" || b.name.startsWith("convertor.")) {
		return "conversion";
	}
	const mapped = OVERHEAD_BUCKET_CATEGORY[b.name];
	if (mapped) {
		return mapped;
	}
	return b.name.startsWith("plugin.") ? "plugins" : "other";
}

// overheadMemberLabel renders a drill-down member with its friendly label when there is
// one, else the span name with the redundant "plugin." prefix stripped (every plugin row
// already sits under the Plugins group).
// Plugin display names where a plain title-case of the kebab id would read wrong
// (acronyms, multi-word tokens). Everything else is title-cased from its id.
const PLUGIN_LABEL_OVERRIDES: Record<string, string> = {
	otel: "workspaces:overhead.plugins.otel",
	datadog: "workspaces:overhead.plugins.datadog",
	compat: "workspaces:overhead.plugins.compat",
	"adaptive-loadbalancer": "workspaces:overhead.plugins.adaptiveLoadBalancer",
	"model-catalog-resolver": "workspaces:overhead.plugins.modelCatalogResolver",
};

// pluginDisplayName turns a plugin's kebab-case id ("enterprise-governance") into a
// friendly label ("Enterprise Governance"), honouring PLUGIN_LABEL_OVERRIDES first.
function pluginDisplayName(id: string, t: TFunction): string {
	const labelKey = PLUGIN_LABEL_OVERRIDES[id];
	if (labelKey) return t(labelKey);
	return id
		.split("-")
		.filter(Boolean)
		.map((w) => w.charAt(0).toUpperCase() + w.slice(1))
		.join(" ");
}

function overheadMemberLabel(name: string, t: TFunction): string {
	const friendly = OVERHEAD_LABEL_KEYS[name];
	if (friendly) return t(friendly);
	if (name.startsWith("plugin.")) return pluginDisplayName(name.slice("plugin.".length), t);
	if (name.startsWith("middleware.")) return name.slice("middleware.".length);
	return name;
}

// buildOverheadCategories groups the raw buckets into the top-level categories,
// ordered largest-first so the bar and legend read like the numbers.
function buildOverheadCategories(buckets: OverheadBucket[]): OverheadCategory[] {
	const grouped = new Map<string, OverheadBucket[]>();
	for (const b of buckets) {
		const key = overheadCategoryKey(b);
		const list = grouped.get(key);
		if (list) list.push(b);
		else grouped.set(key, [b]);
	}
	const cats: OverheadCategory[] = [];
	for (const [key, rawMembers] of grouped) {
		// Fold raw span splits into their merged member (e.g. key-pool -> key.selection),
		// summing durations, before sorting/rendering.
		const byName = new Map<string, OverheadBucket>();
		for (const m of rawMembers) {
			const name = mergedBucketName(m.name);
			const existing = byName.get(name);
			if (existing) existing.duration_us += m.duration_us;
			else byName.set(name, { ...m, name });
		}
		const members = Array.from(byName.values());
		members.sort((a, b) => b.duration_us - a.duration_us);
		cats.push({
			key,
			labelKey: OVERHEAD_CATEGORY_META[key]?.labelKey ?? key,
			colorClass: OVERHEAD_CATEGORY_META[key]?.colorClass ?? "bg-muted-foreground/50",
			totalUs: members.reduce((acc, m) => acc + m.duration_us, 0),
			members,
		});
	}
	return cats.sort((a, b) => b.totalUs - a.totalUs);
}

// OverheadBreakdown renders Bifrost's overhead as a single horizontal stacked bar
// split into the top-level categories, with a legend beneath. Plugin and internal
// spans are measured directly; the "scheduling" bucket (from the backend) accounts for
// the residual goroutine-hop latency between phases, so the segments sum to the full
// overhead number. "View details" expands every category into its individual members
// so a specific phase or plugin can be inspected.
export function OverheadBreakdown({ buckets, overheadMs }: { buckets: OverheadBucket[]; overheadMs?: number }) {
	const { t } = useTranslation(["common", "workspaces"]);
	const [showDetails, setShowDetails] = useState(false);
	if (!buckets || buckets.length === 0) return null;

	const categories = buildOverheadCategories(buckets);
	const sumUs = buckets.reduce((acc, b) => acc + b.duration_us, 0);
	const overheadUs = overheadMs != null && !isNaN(overheadMs) ? overheadMs * 1000 : undefined;

	// When measured spans already exceed the computed overhead, the backend omits a
	// scheduling bucket (it would be negative): a sign the upstream accumulator is
	// over-counting. Surface it rather than let the numbers look inconsistent.
	const overCounted = overheadUs != null && sumUs > overheadUs + 1;

	const barTotal = categories.reduce((acc, c) => acc + c.totalUs, 0) || 1;
	const drillable = categories;

	return (
		<div className="space-y-3">
			<div className="flex items-center justify-between gap-2">
				<BlockHeader title={t("logs:metrics.overheadBreakdown")} />
				<div className="font-mono text-xs tabular-nums">{formatMicros(overheadUs ?? sumUs)}</div>
			</div>

			<div className="flex h-3 w-full overflow-hidden rounded-sm">
				{categories.map((c) => (
					<div
						key={c.key}
						className={cn(c.colorClass, "h-full")}
						style={{ width: `${(c.totalUs / barTotal) * 100}%` }}
						title={`${t(c.labelKey)} · ${formatMicros(c.totalUs)}`}
					/>
				))}
			</div>

			<div className="flex flex-wrap gap-x-4 gap-y-1.5">
				{categories.map((c) => (
					<div key={c.key} className="flex items-center gap-1.5 font-mono text-[11px]">
						<span className={cn("h-2.5 w-2.5 shrink-0 rounded-[2px]", c.colorClass)} />
						<span>{t(c.labelKey)}</span>
						<span className="text-muted-foreground tabular-nums">{formatMicros(c.totalUs)}</span>
					</div>
				))}
			</div>

			{drillable.length > 0 ? (
				<button
					type="button"
					onClick={() => setShowDetails((v) => !v)}
					className="text-muted-foreground hover:text-foreground flex items-center gap-1 font-mono text-[11px] transition"
				>
					{t("common:actions.viewDetails")}
					<ChevronDown className={cn("h-3 w-3 transition-transform", showDetails ? "rotate-180" : "rotate-0")} />
				</button>
			) : null}

			{showDetails ? (
				<div className="space-y-3 pt-1">
					{drillable.map((c) => (
						<div key={c.key} className="space-y-1.5">
							<div className="text-muted-foreground flex items-center gap-1.5 text-[11px] font-medium tracking-wide uppercase">
								<span className={cn("h-2 w-2 shrink-0 rounded-[2px]", c.colorClass)} />
								{t(c.labelKey)}
								{/* normal-case: keep the unit as "µs" — uppercasing mangles the micro sign into "ΜS" (reads as ms) */}
								<span className="normal-case tabular-nums">{formatMicros(c.totalUs)}</span>
							</div>
							<div className="space-y-1 pl-3">
								{c.members.map((m) => (
									<div key={m.name} className="flex items-center justify-between gap-3 font-mono text-[11px]">
										<span className="truncate" title={m.name}>
											{overheadMemberLabel(m.name, t)}
										</span>
										<span className="text-muted-foreground shrink-0 tabular-nums">{formatMicros(m.duration_us)}</span>
									</div>
								))}
							</div>
						</div>
					))}
				</div>
			) : null}

			{overCounted ? (
				<div className="text-muted-foreground text-[11px]">
					{t("workspaces:overhead.overCounted", { measured: formatMicros(sumUs), computed: formatMicros(overheadUs!) })}
				</div>
			) : null}
		</div>
	);
}