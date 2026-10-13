/**
 * Routing Rule Dialog (Sheet)
 * Create/Edit form for routing rules
 */

import { CustomerSelector } from "@/components/entitySelectors/customerSelector";
import { TeamSelector } from "@/components/entitySelectors/teamSelector";
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector, type ProviderSelectorOption } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { resolveProviderIconKey } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getUserPicker } from "@/lib/registries/userPicker";
import { getErrorMessage } from "@/lib/store";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { useCreateRoutingRuleMutation, useGetRoutingRulesQuery, useUpdateRoutingRuleMutation } from "@/lib/store/apis/routingRulesApi";
import {
	DEFAULT_ROUTING_FALLBACK,
	DEFAULT_ROUTING_RULE_FORM_DATA,
	DEFAULT_ROUTING_TARGET,
	ROUTING_RULE_SCOPES,
	RoutingFallbackFormData,
	RoutingRule,
	RoutingRuleFormData,
	RoutingTargetFormData,
} from "@/lib/types/routingRules";
import {
	denormalizeFallback,
	MAX_TTFT_TIMEOUT_MS,
	normalizeFallback,
	parseTTFTTimeoutInput,
	resolveTargetTTFTMs,
	summarizeTargetsTTFT,
	summarizeTTFTDisplay,
} from "@/lib/utils/routingRules";
import { validateRateLimitAndBudgetRules, validateRoutingRules } from "@/lib/utils/celConverterRouting";
import { isValidRuleGroupType, normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Plus, Trash2, X } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import i18n from "@/lib/i18n";
import { RuleGroupType } from "react-querybuilder";
import { toast } from "sonner";
// Side-effect import: registers the enterprise user picker (no-op in OSS builds).
import "@enterprise/lib/registrations/userPicker";

interface RoutingRuleDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingRule?: RoutingRule | null;
	onSuccess?: () => void;
}

const defaultQuery: RuleGroupType = {
	combinator: "and",
	rules: [],
};

type ConditionMode = "builder" | "cel";

/**
 * Decides which conditions editor a rule opens in. Rules authored outside the visual
 * builder (e.g. via the API) have a CEL expression but no usable `query`; those open in
 * CEL mode so the expression stays visible and editable instead of being silently cleared.
 */
function initialConditionMode(rule?: RoutingRule | null): ConditionMode {
	if (!rule) {
		return "builder";
	}
	const hasQuery = isValidRuleGroupType(rule.query) && (rule.query.rules?.length ?? 0) > 0;
	if (hasQuery) {
		return "builder";
	}
	return rule.cel_expression?.trim() ? "cel" : "builder";
}

// Lazy-load CEL builder (heavy dependency tree).
const CELRuleBuilderLazy = lazy(() =>
	import("@/app/workspace/routing-rules/components/celBuilder/celRuleBuilder").then((mod) => ({
		default: mod.CELRuleBuilder,
	})),
);
const CELRuleBuilder = (props: React.ComponentProps<typeof CELRuleBuilderLazy>) => (
	<Suspense fallback={<div className="text-sm text-gray-500">{i18n.t("governance:routingRules.loadingCelBuilder")}</div>}>
		<CELRuleBuilderLazy {...props} />
	</Suspense>
);

export function RoutingRuleSheet({ open, onOpenChange, editingRule, onSuccess }: RoutingRuleDialogProps) {
	const { t } = useTranslation(["common", "governance"]);
	const { data: rulesData } = useGetRoutingRulesQuery();
	const rules = rulesData?.rules || [];
	const { data: providersData = [] } = useGetProvidersQuery();
	const { data: allKeysData = [] } = useGetAllKeysQuery();
	const [createRoutingRule, { isLoading: isCreating }] = useCreateRoutingRuleMutation();
	const [updateRoutingRule, { isLoading: isUpdating }] = useUpdateRoutingRuleMutation();

	// State for targets and query (managed outside react-hook-form for complex nested structures)
	const [targets, setTargets] = useState<RoutingTargetFormData[]>([{ ...DEFAULT_ROUTING_TARGET }]);
	const [query, setQuery] = useState<RuleGroupType>(defaultQuery);
	const [conditionMode, setConditionMode] = useState<ConditionMode>("builder");
	const [builderKey, setBuilderKey] = useState(0);
	// Server-side CEL compile error, surfaced inline under the CEL editor instead of a toast.
	const [celError, setCelError] = useState<string | null>(null);

	const {
		register,
		handleSubmit,
		setValue,
		watch,
		reset,
		formState: { errors },
	} = useForm<RoutingRuleFormData>({
		defaultValues: DEFAULT_ROUTING_RULE_FORM_DATA,
	});

	// The TTFT input text as loaded from the rule; lets submit tell "untouched" from "edited".
	const loadedTTFTInput = useRef("");
	const [ttftEdited, setTtftEdited] = useState(false); // set once the user types in the field

	const isEditing = !!editingRule;
	const isLoading = isCreating || isUpdating;
	const canCreate = useRbac(RbacResource.RoutingRules, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);
	const hasRequiredAccess = isEditing ? canUpdate : canCreate;
	const enabled = watch("enabled");
	const chainRule = watch("chain_rule");
	const scope = watch("scope");
	const scopeId = watch("scope_id");

	// Registered by the downstream build at module load; undefined in builds
	// without a user directory, which hides the "User" scope option.
	const UserPicker = getUserPicker();
	const fallbacks = watch("fallbacks");
	const ttftTimeoutInput = watch("ttft_timeout_ms");
	const ttftDisplay = summarizeTTFTDisplay(editingRule?.targets, ttftTimeoutInput, ttftEdited);
	const hasCompleteFallback = (fallbacks || []).some((fb) => (fb.provider ?? "").trim().length > 0);

	// The selector lists the configured providers on its own. These are the extras: a
	// provider the current targets, another rule's targets, or a fallback still names after
	// it was deleted, so an existing rule keeps rendering what it actually points at.
	const configuredNames = useMemo(() => new Set<string>(providersData.map((p) => p.name)), [providersData]);
	const referencedNames = useMemo(
		() =>
			new Set<string>([
				...(targets.map((t) => t.provider).filter(Boolean) as string[]),
				...(rules.flatMap((r) => r.targets?.map((t) => t.provider).filter(Boolean) ?? []) as string[]),
				...rules.flatMap((r) => (r.fallbacks ?? []).map((f) => normalizeFallback(f).provider?.trim()).filter(Boolean) as string[]),
			]),
		[targets, rules],
	);
	const referencedProviderOptions = useMemo<ProviderSelectorOption[]>(
		() =>
			Array.from(referencedNames)
				.filter((name) => !configuredNames.has(name))
				.map((name) => ({ value: name, label: getProviderLabel(name), iconKey: resolveProviderIconKey(name) })),
		[referencedNames, configuredNames],
	);

	// The CEL builder still needs the flat union: it offers providers as literal values in an
	// expression, where one a rule already names has to stay offerable.
	const availableProviders = useMemo(
		() => Array.from(new Set<string>([...configuredNames, ...referencedNames])),
		[configuredNames, referencedNames],
	);

	// Initialize form data when editing rule changes
	useEffect(() => {
		if (editingRule) {
			setValue("id", editingRule.id);
			setValue("name", editingRule.name);
			setValue("description", editingRule.description);
			setValue("cel_expression", editingRule.cel_expression);
			setValue("fallbacks", (editingRule.fallbacks || []).map(normalizeFallback));
			loadedTTFTInput.current = summarizeTargetsTTFT(editingRule.targets).ms?.toString() ?? "";
			setTtftEdited(false);
			setValue("ttft_timeout_ms", loadedTTFTInput.current);
			setValue("scope", editingRule.scope);
			setValue("scope_id", editingRule.scope_id || "");
			setValue("priority", editingRule.priority);
			setValue("enabled", editingRule.enabled);
			setValue("chain_rule", editingRule.chain_rule ?? false);
			if (editingRule.targets && editingRule.targets.length > 0) {
				setTargets(
					editingRule.targets.map((t) => ({
						...DEFAULT_ROUTING_TARGET,
						provider: t.provider || "",
						model: t.model || "",
						key_id: t.key_id || "",
						weight: t.weight,
						ttft_timeout_ms: t.ttft_timeout_ms || undefined,
					})),
				);
			} else {
				setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
			}
			// Only react-querybuilder-shaped queries are valid; config may store other JSON under `query`.
			setQuery(normalizeRoutingRuleGroupQuery(editingRule.query));
			setConditionMode(initialConditionMode(editingRule));
			setBuilderKey((prev) => prev + 1);
			setCelError(null);
		} else {
			loadedTTFTInput.current = "";
			setTtftEdited(false);
			reset();
			setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
			setQuery(defaultQuery);
			setConditionMode("builder");
			setBuilderKey((prev) => prev + 1);
			setCelError(null);
		}
	}, [editingRule, open, setValue, reset]);

	const handleQueryChange = useCallback(
		(expression: string, newQuery: RuleGroupType) => {
			setValue("cel_expression", expression);
			setQuery(newQuery);
			// Editing the expression clears a stale server-side CEL error.
			setCelError(null);
		},
		[setValue],
	);

	const handleModeChange = useCallback((mode: ConditionMode) => {
		setConditionMode(mode);
		setCelError(null);
	}, []);

	const addTarget = () => {
		const remaining = 1 - targets.reduce((sum, t) => sum + (t.weight || 0), 0);
		setTargets((prev) => [
			...prev,
			{
				...DEFAULT_ROUTING_TARGET,
				weight: Math.max(0, parseFloat(remaining.toFixed(4))),
			},
		]);
	};

	const removeTarget = (index: number) => {
		setTargets((prev) => prev.filter((_, i) => i !== index));
	};

	const updateTarget = (index: number, field: keyof RoutingTargetFormData, value: string | number) => {
		setTargets((prev) => prev.map((t, i) => (i === index ? { ...t, [field]: value } : t)));
	};

	const updateFallback = (index: number, changes: Partial<RoutingFallbackFormData>) => {
		setValue(
			"fallbacks",
			(fallbacks || []).map((fb, i) => (i === index ? { ...fb, ...changes } : fb)),
		);
	};

	const removeFallback = (index: number) => {
		setValue(
			"fallbacks",
			(fallbacks || []).filter((_, i) => i !== index),
		);
	};

	const totalWeight = targets.reduce((sum, t) => sum + (t.weight || 0), 0);

	const onSubmit = (data: RoutingRuleFormData) => {
		setCelError(null);

		// Validate scope_id is required when scope is not global
		if (data.scope !== "global" && !data.scope_id?.trim()) {
			toast.error(
				t("governance:routingRules.sheet.toastScopeRequired", {
					scope:
						data.scope === "team"
							? t("governance:routingRules.sheet.scopeTeam")
							: data.scope === "customer"
								? t("governance:routingRules.sheet.scopeCustomer")
								: data.scope === "user"
									? t("governance:routingRules.sheet.scopeUser")
									: t("governance:routingRules.sheet.scopeVirtualKey"),
				}),
			);
			return;
		}

		// Validate targets
		if (targets.length === 0) {
			toast.error(t("governance:routingRules.sheet.toastTargetRequired"));
			return;
		}
		for (const tgt of targets) {
			if (tgt.weight <= 0) {
				toast.error(t("governance:routingRules.sheet.toastWeightPositive"));
				return;
			}
		}
		if (Math.abs(totalWeight - 1) > 0.001) {
			toast.error(t("governance:routingRules.sheet.toastWeightSum", { total: totalWeight.toFixed(4) }));
			return;
		}

		// Builder-only validation: these inspect the visual query, which does not exist in
		// raw-CEL mode. In CEL mode the expression is validated server-side on save instead.
		if (conditionMode === "builder") {
			// Validate regex patterns in routing rules
			const regexErrors = validateRoutingRules(query);
			if (regexErrors.length > 0) {
				toast.error(t("governance:routingRules.sheet.toastInvalidRegex", { errors: regexErrors.join("\n") }));
				return;
			}

			// Validate rate limit and budget rules
			const rateLimitErrors = validateRateLimitAndBudgetRules(query);
			if (rateLimitErrors.length > 0) {
				toast.error(t("governance:routingRules.sheet.toastInvalidConfig", { errors: rateLimitErrors.join("\n") }));
				return;
			}
		}

		// Filter out incomplete fallbacks (empty provider)
		const validFallbacks = (data.fallbacks || []).filter((fb) => (fb.provider ?? "").trim().length > 0).map(denormalizeFallback);

		const payload = {
			name: data.name,
			description: data.description,
			cel_expression: data.cel_expression,
			targets: targets.map(({ provider, model, key_id, weight, ttft_timeout_ms }) => ({
				provider: provider || undefined,
				model: model || undefined,
				key_id: key_id || undefined,
				weight,
				// The API stores the deadline per target. Untouched input keeps each target's own value;
				// an edit applies to all of them. 0 turns it off, and clears a stored one on update.
				ttft_timeout_ms: resolveTargetTTFTMs(data.ttft_timeout_ms, loadedTTFTInput.current, ttft_timeout_ms, ttftEdited),
			})),
			fallbacks: validFallbacks,
			scope: data.scope,
			scope_id: data.scope === "global" ? undefined : data.scope_id || undefined,
			priority: data.priority,
			enabled: data.enabled,
			chain_rule: data.chain_rule,
			query: query,
		};

		const submitPromise =
			isEditing && editingRule
				? updateRoutingRule({
						id: editingRule.id,
						data: payload,
					}).unwrap()
				: createRoutingRule(payload).unwrap();

		submitPromise
			.then(() => {
				toast.success(isEditing ? t("governance:routingRules.sheet.toastUpdated") : t("governance:routingRules.sheet.toastCreated"));
				reset();
				setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
				setQuery(defaultQuery);
				setConditionMode("builder");
				setBuilderKey((prev) => prev + 1);
				setCelError(null);
				onOpenChange(false);
				onSuccess?.();
			})
			.catch((error: any) => {
				const message = getErrorMessage(error);
				// A malformed CEL expression is a field-level problem — show it beneath the CEL
				// editor rather than in a toast (which turns a syntax error into a jarring popup).
				if (conditionMode === "cel" && /cel expression/i.test(message)) {
					setCelError(message);
					return;
				}
				toast.error(message);
			});
	};

	const handleCancel = () => {
		reset();
		setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
		setQuery(defaultQuery);
		setConditionMode("builder");
		setBuilderKey((prev) => prev + 1);
		setCelError(null);
		onOpenChange(false);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full min-w-1/2 flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						{isEditing ? t("governance:routingRules.sheet.editTitle") : t("governance:routingRules.sheet.createTitle")}
					</SheetTitle>
					<SheetDescription>
						{isEditing ? t("governance:routingRules.sheet.editDescription") : t("governance:routingRules.sheet.createDescription")}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex grow flex-col">
					<div className="flex grow flex-col gap-6 px-4 pb-6 md:px-8">
						{/* Rule Name */}
						<div className="space-y-3">
							<Label htmlFor="name">
								{t("governance:routingRules.ruleName")} <span className="text-red-500">*</span>
							</Label>
							<Input
								id="name"
								placeholder={t("governance:routingRules.sheet.namePlaceholder")}
								{...register("name", {
									required: t("governance:routingRules.sheet.nameRequired"),
									maxLength: 255,
								})}
							/>
							{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						</div>

						{/* Description */}
						<div className="space-y-3">
							<Label htmlFor="description">{t("governance:routingRules.sheet.descriptionLabel")}</Label>
							<Textarea
								id="description"
								placeholder={t("governance:routingRules.sheet.descriptionPlaceholder")}
								rows={2}
								{...register("description")}
							/>
						</div>

						{/* Enabled Switch */}
						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="enabled">{t("governance:routingRules.sheet.enableRule")}</Label>
								<p className="text-muted-foreground text-sm">{t("governance:routingRules.activeHint")}</p>
							</div>
							<Switch id="enabled" checked={enabled} onCheckedChange={(checked) => setValue("enabled", checked)} />
						</div>

						{/* Chain Rule Switch */}
						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="chain_rule">{t("governance:routingRules.sheet.chainRule")}</Label>
								<p className="text-muted-foreground text-sm">{t("governance:routingRules.sheet.chainRuleHelp")}</p>
							</div>
							<Switch
								id="chain_rule"
								checked={chainRule}
								onCheckedChange={(checked) => setValue("chain_rule", checked)}
								data-testid="routing-rule-chain-rule-switch"
							/>
						</div>

						{/* Scope and Priority - Side by Side */}
						<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
							<div className="space-y-3">
								<Label htmlFor="scope">{t("governance:routingRules.sheet.scopeLabel")}</Label>
								<Select
									value={scope}
									onValueChange={(value) => {
										setValue("scope", value as any);
										// Clear scope_id when scope changes
										setValue("scope_id", "");
									}}
								>
									<SelectTrigger className="w-full">
										<SelectValue placeholder={t("governance:routingRules.sheet.scopePlaceholder")} />
									</SelectTrigger>
									<SelectContent>
										{ROUTING_RULE_SCOPES.map((scopeOption) => (
											<SelectItem key={scopeOption.value} value={scopeOption.value}>
												{t(
													`governance:routingRules.sheet.scope${scopeOption.value === "virtual_key" ? "VirtualKey" : scopeOption.value === "global" ? "Global" : scopeOption.value === "team" ? "Team" : "Customer"}`,
												)}
											</SelectItem>
										))}
										{(UserPicker || scope === "user") && (
											<SelectItem value="user">{t("governance:routingRules.sheet.scopeUser")}</SelectItem>
										)}
									</SelectContent>
								</Select>
							</div>

							<div className="space-y-3">
								<Label htmlFor="priority">
									{t("governance:routingRules.sheet.priorityLabel")} <span className="text-red-500">*</span>
								</Label>
								<Input
									id="priority"
									type="number"
									min={0}
									max={1000}
									{...register("priority", {
										required: t("governance:routingRules.sheet.priorityRequired"),
										min: { value: 0, message: t("governance:routingRules.sheet.priorityMin") },
										max: { value: 1000, message: t("governance:routingRules.sheet.priorityMax") },
										valueAsNumber: true,
									})}
								/>
								<p className="text-muted-foreground text-xs">{t("governance:routingRules.sheet.priorityHint")}</p>
								{errors.priority && <p className="text-destructive text-sm">{errors.priority.message}</p>}
							</div>
						</div>

						{scope !== "global" && (
							<div className="space-y-2">
								<Label htmlFor="scope_id">
									{t("governance:routingRules.sheet.scopeIdSuffix", {
										scope:
											scope === "team"
												? t("governance:routingRules.sheet.scopeTeam")
												: scope === "customer"
													? t("governance:routingRules.sheet.scopeCustomer")
													: scope === "user"
														? t("governance:routingRules.sheet.scopeUser")
														: t("governance:routingRules.sheet.scopeVirtualKey"),
									})}{" "}
									<span className="text-red-500">*</span>
								</Label>
								{/* A rule stores only its scope_id, so there is no name to seed
								    these with — each selector resolves its own selection. */}
								{scope === "team" && <TeamSelector value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />}
								{scope === "customer" && <CustomerSelector value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />}
								{scope === "virtual_key" && <VirtualKeySelector value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />}
								{scope === "user" &&
									(UserPicker ? (
										<UserPicker value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />
									) : (
										// No user directory in this build: keep a plain input so
										// existing user-scoped rules remain editable.
										<Input
											id="scope_id"
											data-testid="routing-rule-scope-user-input"
											placeholder={t("governance:routingRules.sheet.scopeUserIdPlaceholder")}
											value={scopeId || ""}
											onChange={(e) => setValue("scope_id", e.target.value)}
										/>
									))}
								{/* Teams, customers and virtual keys are all searched lazily inside their
								    selectors, each of which surfaces its own empty state. */}
								{errors.scope_id && <p className="text-destructive text-sm">{errors.scope_id.message}</p>}
							</div>
						)}

						<Separator />

						{/* CEL Rule Builder */}
						<div className="space-y-3">
							<Label>{t("governance:routingRules.sheet.ruleBuilder")}</Label>
							<p className="text-muted-foreground text-sm">{t("governance:routingRules.sheet.ruleBuilderHelp")}</p>
							<CELRuleBuilder
								key={builderKey}
								initialQuery={query}
								onChange={handleQueryChange}
								providers={availableProviders}
								models={[]}
								allowCustomModels={true}
								allowCelMode={true}
								initialMode={conditionMode}
								initialCel={editingRule?.cel_expression ?? ""}
								onModeChange={handleModeChange}
								celError={celError}
							/>
						</div>

						{/* Note about Token/Request Limits and Budget Configuration */}
						<p className="text-muted-foreground text-xs">
							{t("governance:routingRules.sheet.limitsNote")} <strong>{t("governance:routingRules.sheet.limitsNoteProvider")}</strong>{" "}
							{t("governance:routingRules.sheet.limitsNoteProviderSuffix")}{" "}
							<strong>{t("governance:routingRules.sheet.limitsNoteModel")}</strong> {t("governance:routingRules.sheet.limitsNoteTail")}
						</p>

						<Separator />

						{/* Routing Targets */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<div>
									<Label>{t("governance:routingRules.sheet.routingTargets")}</Label>
									<p className="text-muted-foreground mt-0.5 text-xs">{t("governance:routingRules.sheet.routingTargetsHelp")}</p>
								</div>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={addTarget}
									className="shrink-0 gap-2"
									data-testid="routing-rule-target-add"
								>
									<Plus className="h-4 w-4" />
									{t("governance:routingRules.sheet.addTarget")}
								</Button>
							</div>

							<div className="space-y-3">
								{targets.map((target, index) => (
									<TargetRow
										key={index}
										target={target}
										index={index}
										referencedProviderOptions={referencedProviderOptions}
										allKeys={allKeysData}
										showRemove={targets.length > 1}
										onUpdate={updateTarget}
										onRemove={removeTarget}
									/>
								))}
							</div>

							{/* Weight sum indicator */}
							<div
								className={`flex items-center justify-end gap-2 text-xs font-medium ${Math.abs(totalWeight - 1) > 0.001 ? "text-destructive" : "text-muted-foreground"}`}
							>
								{t("governance:routingRules.sheet.totalWeight", { total: totalWeight.toFixed(4) })}
								{Math.abs(totalWeight - 1) > 0.001 && (
									<span className="text-destructive">{t("governance:routingRules.sheet.mustEqualOne")}</span>
								)}
							</div>
						</div>

						{/* Fallbacks */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<div>
									<Label>{t("governance:routingRules.sheet.fallbacks")}</Label>{" "}
									<p className="text-muted-foreground mt-0.5 text-xs">{t("governance:routingRules.sheet.fallbacksHelp")}</p>
								</div>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => setValue("fallbacks", [...(fallbacks || []), { ...DEFAULT_ROUTING_FALLBACK }])}
									className="gap-2"
								>
									<Plus className="h-4 w-4" />
									{t("governance:routingRules.sheet.addFallback")}
								</Button>
							</div>
							<div className="space-y-2">
								{(fallbacks || []).length === 0 ? (
									<p className="text-muted-foreground text-sm">{t("governance:routingRules.noFallbacks")}</p>
								) : (
									(fallbacks || []).map((fallback, index) => (
										<FallbackRow
											key={index}
											fallback={fallback}
											index={index}
											referencedProviderOptions={referencedProviderOptions}
											allKeys={allKeysData}
											onUpdate={updateFallback}
											onRemove={removeFallback}
										/>
									))
								)}
							</div>
							<p className="text-muted-foreground text-xs">{t("governance:routingRules.sheet.fallbacksOrderNote")}</p>
						</div>

						{/* TTFT cutoff */}
						<div className="space-y-3">
							<Label htmlFor="ttft_timeout_ms">{t("governance:routingRules.sheet.ttftLabel")}</Label>
							<Input
								id="ttft_timeout_ms"
								type="text"
								inputMode="numeric"
								placeholder={ttftDisplay.mixed ? t("governance:routingRules.sheet.ttftMixed") : t("governance:routingRules.sheet.ttftOff")}
								aria-invalid={errors.ttft_timeout_ms ? true : undefined}
								aria-describedby={errors.ttft_timeout_ms ? "ttft_timeout_ms-error" : undefined}
								data-testid="routing-rule-ttft-timeout-input"
								{...register("ttft_timeout_ms", {
									onChange: () => {
										setTtftEdited(true);
									},
									validate: (value) =>
										parseTTFTTimeoutInput(value) !== null || t("governance:routingRules.sheet.ttftValidation", { max: MAX_TTFT_TIMEOUT_MS }),
								})}
							/>
							<p className="text-muted-foreground text-xs">{t("governance:routingRules.sheet.ttftHelp")}</p>
							{errors.ttft_timeout_ms && (
								<p id="ttft_timeout_ms-error" className="text-destructive text-sm" data-testid="routing-rule-ttft-timeout-error">
									{errors.ttft_timeout_ms.message}
								</p>
							)}
							{!errors.ttft_timeout_ms && ttftDisplay.active && !hasCompleteFallback && (
								<p className="text-sm text-amber-600 dark:text-amber-500" data-testid="routing-rule-ttft-timeout-no-fallback-warning">
									{t("governance:routingRules.sheet.ttftNoFallbackNote")}
								</p>
							)}
						</div>
					</div>
					{/* Action Buttons */}
					<div className="bg-card sticky bottom-0 flex justify-end gap-3 border-t px-4 py-4 md:px-8">
						<Button type="button" variant="outline" onClick={handleCancel} disabled={isLoading}>
							{t("common:actions.cancel")}
						</Button>
						<Button type="submit" disabled={isLoading || !hasRequiredAccess}>
							{isEditing ? t("governance:routingRules.sheet.updateRule") : t("governance:routingRules.sheet.saveRule")}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}

interface ProviderKeySelectProps {
	idPrefix: string;
	clearLabel: string;
	provider?: string;
	keyId?: string;
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onChange: (keyId: string) => void;
}

/** Renders nothing until a provider is chosen, since keys are scoped to one. */
function ProviderKeySelect({ idPrefix, clearLabel, provider, keyId, allKeys, onChange }: ProviderKeySelectProps) {
	const { t } = useTranslation(["common", "governance"]);
	const availableKeys = provider ? allKeys.filter((k) => k.provider === provider).map((k) => ({ id: k.key_id, name: k.name })) : [];
	if (!provider || (availableKeys.length === 0 && !keyId)) {
		return null;
	}

	return (
		<div className="space-y-1.5">
			<Label id={`${idPrefix}-apikey-label`} className="text-xs">
				{t("governance:routingRules.sheet.apiKeyLabel")}{" "}
				<span className="text-muted-foreground">{t("governance:routingRules.sheet.apiKeyOptionalNote")}</span>
			</Label>
			<div className="flex gap-1.5">
				<Select value={keyId || ""} onValueChange={onChange}>
					<SelectTrigger
						id={`${idPrefix}-apikey-select`}
						aria-labelledby={`${idPrefix}-apikey-label`}
						className="h-9 flex-1 text-sm"
						data-testid={`${idPrefix}-apikey-select`}
					>
						<SelectValue placeholder={t("governance:routingRules.sheet.selectKeyOptional")} />
					</SelectTrigger>
					<SelectContent>
						{availableKeys.map((key) => (
							<SelectItem key={key.id} value={key.id}>
								{key.name}
							</SelectItem>
						))}
						{keyId && !availableKeys.some((k) => k.id === keyId) && (
							<SelectItem key={`pinned-${keyId}`} value={keyId}>
								{t("governance:routingRules.sheet.pinnedKey", { keyId })}
							</SelectItem>
						)}
					</SelectContent>
				</Select>
				{keyId && (
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onChange("")}
						className="h-9 w-9 p-0"
						aria-label={clearLabel}
						data-testid={`${idPrefix}-apikey-clear`}
					>
						<X className="h-3.5 w-3.5" />
					</Button>
				)}
			</div>
		</div>
	);
}

interface FallbackRowProps {
	fallback: RoutingFallbackFormData;
	index: number;
	referencedProviderOptions: ProviderSelectorOption[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onUpdate: (index: number, changes: Partial<RoutingFallbackFormData>) => void;
	onRemove: (index: number) => void;
}

function FallbackRow({ fallback, index, referencedProviderOptions, allKeys, onUpdate, onRemove }: FallbackRowProps) {
	const { t } = useTranslation(["common", "governance"]);
	const provider = fallback.provider || "";

	return (
		<div className="space-y-2 rounded-lg border p-3" data-testid={`routing-fallback-${index}`}>
			<div className="flex items-center gap-2">
				<div className="flex-1">
					<ProviderSelector
						extraOptions={referencedProviderOptions}
						value={provider}
						// A key belongs to one provider, so switching providers invalidates the pin.
						onChange={(value: string) => onUpdate(index, { provider: value, model: "", key_id: "" })}
						placeholder={t("governance:routingRules.sheet.selectProvider")}
						className="!h-9 !min-h-9"
						data-testid={`routing-fallback-${index}-provider-select`}
						noPortal
					/>
				</div>
				<div className="flex-1" data-testid={`routing-fallback-${index}-model-select`}>
					<ModelSelector
						provider={provider || undefined}
						value={fallback.model || ""}
						onChange={(value) => onUpdate(index, { model: value })}
						placeholder={t("governance:routingRules.sheet.incomingOptional")}
						allowCustomModel
						unfiltered={true}
						disabled={!provider}
						className="!h-9 !min-h-9 w-full"
					/>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					onClick={() => onRemove(index)}
					className="h-9 px-2"
					aria-label={t("governance:routingRules.sheet.removeFallbackAria", { n: index + 1 })}
					data-testid={`routing-fallback-${index}-remove-button`}
				>
					<Trash2 className="h-4 w-4" />
				</Button>
			</div>

			<ProviderKeySelect
				idPrefix={`routing-fallback-${index}`}
				clearLabel={t("governance:routingRules.sheet.clearApiKeyFallbackAria", { n: index + 1 })}
				provider={provider}
				keyId={fallback.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, { key_id: value })}
			/>
		</div>
	);
}

interface TargetRowProps {
	target: RoutingTargetFormData;
	index: number;
	referencedProviderOptions: ProviderSelectorOption[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	showRemove: boolean;
	onUpdate: (index: number, field: keyof RoutingTargetFormData, value: string | number) => void;
	onRemove: (index: number) => void;
}

function TargetRow({ target, index, referencedProviderOptions, allKeys, showRemove, onUpdate, onRemove }: TargetRowProps) {
	const { t } = useTranslation(["common", "governance"]);
	return (
		<div className="space-y-3 rounded-lg border p-3" data-testid={`routing-target-${index}`}>
			<div className="flex items-center justify-between">
				<span className="text-muted-foreground text-sm font-medium">{t("governance:routingRules.sheet.targetN", { n: index + 1 })}</span>
				<div className="flex items-center gap-2">
					<div className="flex items-center gap-1.5">
						<Label htmlFor={`routing-target-${index}-weight-input`} className="text-muted-foreground shrink-0 text-xs">
							{t("governance:routingRules.weight")}
						</Label>
						<Input
							id={`routing-target-${index}-weight-input`}
							type="number"
							min={0.001}
							max={1}
							step={0.001}
							value={target.weight}
							onChange={(e) => onUpdate(index, "weight", parseFloat(e.target.value) || 0)}
							className="h-8 w-24 text-sm"
							data-testid={`routing-target-${index}-weight-input`}
						/>
					</div>
					{showRemove && (
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => onRemove(index)}
							className="h-8 w-8 p-0"
							aria-label={t("governance:routingRules.sheet.removeTargetAria", { n: index + 1 })}
							data-testid={`routing-target-${index}-remove-button`}
						>
							<Trash2 className="h-3.5 w-3.5" />
						</Button>
					)}
				</div>
			</div>

			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-provider-label`} className="text-xs">
						{t("common:fields.provider")}
					</Label>
					<div className="flex gap-1.5">
						<ProviderSelector
							extraOptions={referencedProviderOptions}
							value={target.provider || ""}
							onChange={(value: string) => {
								onUpdate(index, "provider", value);
								onUpdate(index, "model", "");
								onUpdate(index, "key_id", "");
							}}
							placeholder={t("governance:routingRules.sheet.incomingOptional")}
							className="!h-9 !min-h-9 flex-1 text-sm"
							data-testid={`routing-target-${index}-provider-select`}
							noPortal
						/>
						{target.provider && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => {
									onUpdate(index, "provider", "");
									onUpdate(index, "model", "");
									onUpdate(index, "key_id", "");
								}}
								className="h-9 w-9 p-0"
								aria-label={t("governance:routingRules.sheet.clearProviderTargetAria", { n: index + 1 })}
								data-testid={`routing-target-${index}-provider-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>

				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-model-label`} className="text-xs">
						{t("common:fields.model")}
					</Label>
					<div className="flex gap-1.5">
						<div className="flex-1" data-testid={`routing-target-${index}-model-select`}>
							<ModelSelector
								provider={target.provider || undefined}
								value={target.model}
								onChange={(value) => onUpdate(index, "model", value)}
								placeholder={t("governance:routingRules.sheet.incomingOptional")}
								allowCustomModel
								className="!h-9 !min-h-9"
								inputId={`routing-target-${index}-model-input`}
								ariaLabelledBy={`routing-target-${index}-model-label`}
							/>
						</div>
						{target.model && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => onUpdate(index, "model", "")}
								className="h-9 w-9 p-0"
								aria-label={t("governance:routingRules.sheet.clearModelTargetAria", { n: index + 1 })}
								data-testid={`routing-target-${index}-model-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>
			</div>

			<ProviderKeySelect
				idPrefix={`routing-target-${index}`}
				clearLabel={t("governance:routingRules.sheet.clearApiKeyTargetAria", { n: index + 1 })}
				provider={target.provider}
				keyId={target.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, "key_id", value)}
			/>
		</div>
	);
}