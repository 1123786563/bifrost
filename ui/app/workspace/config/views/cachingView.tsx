import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import {
	getErrorMessage,
	useCreatePluginMutation,
	useGetCoreConfigQuery,
	useGetPluginsQuery,
	useGetProvidersQuery,
	useUpdatePluginMutation,
} from "@/lib/store";
import { CacheConfig, EditorCacheConfig, ModelProvider, ModelProviderName } from "@/lib/types/config";
import { SEMANTIC_CACHE_PLUGIN } from "@/lib/types/plugins";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib/contexts/rbacContext";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";

// The local cache plugin runs in one of two modes. Direct-only is purely
// hash-based, no embedding provider needed; perfect for exact-replay
// caching. Semantic adds vector similarity on top, requiring an
// embedding-capable provider and the model's real dimension.
type CacheMode = "direct" | "semantic";

// Embedding-capable providers gate the semantic mode. Built-in providers
// are listed in EmbeddingSupportedProviders; custom providers expose
// support via custom_provider_config.allowed_requests.embedding.
const supportsEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		return provider.custom_provider_config.allowed_requests?.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

const defaultDirectConfig: EditorCacheConfig = {
	ttl: 300,
	threshold: 0.8,
	dimension: 1,
	conversation_history_threshold: 3,
	exclude_system_prompt: false,
	cache_by_model: true,
	cache_by_provider: true,
	disable_identity_scoping: false,
};

// Configs we treat as "the user has nothing saved": both API responses
// where every field is the type's zero value and the literal undefined
// look like this.
const isEmptyConfig = (config: Partial<EditorCacheConfig> | undefined): boolean => {
	if (!config) return true;
	// Booleans are deliberate user choices (e.g. cache_by_model: false), not
	// empty markers — only treat numeric/string zero values as empty.
	const isZero = (v: unknown) => v === undefined || v === null || v === 0 || v === "";
	return Object.values(config).every(isZero);
};

const toEditorCacheConfig = (config?: Partial<EditorCacheConfig>): EditorCacheConfig => {
	if (!config || isEmptyConfig(config)) {
		return { ...defaultDirectConfig };
	}
	return { ...defaultDirectConfig, ...config };
};

const inferMode = (config: EditorCacheConfig): CacheMode => {
	if (config.dimension && config.dimension > 1 && config.provider) return "semantic";
	return "direct";
};

// Clear semantic-only fields when persisting a direct-only payload so the
// server validator doesn't reject a stale provider choice. An update merges
// over the saved config, so they are sent empty rather than left out.
const buildPayload = (config: EditorCacheConfig, mode: CacheMode): CacheConfig => {
	const base = {
		ttl: config.ttl ?? 0,
		threshold: config.threshold ?? 0,
		conversation_history_threshold: config.conversation_history_threshold,
		exclude_system_prompt: config.exclude_system_prompt,
		cache_by_model: config.cache_by_model,
		cache_by_provider: config.cache_by_provider,
		disable_identity_scoping: config.disable_identity_scoping ?? false,
		vector_store_namespace: config.vector_store_namespace?.trim() || undefined,
		default_cache_key: config.default_cache_key?.trim() || undefined,
	};
	if (mode === "direct") {
		return { ...base, provider: "", embedding_model: "", dimension: 1 } as CacheConfig;
	}
	return {
		...base,
		provider: config.provider as ModelProviderName,
		embedding_model: config.embedding_model ?? "",
		dimension: config.dimension ?? 0,
	} as CacheConfig;
};

const validateForSave = (config: EditorCacheConfig, mode: CacheMode): string | null => {
	if (mode === "semantic") {
		if (!config.provider) return "settings:cache.validation.pickProvider";
		if (!config.embedding_model?.trim()) return "settings:cache.validation.pickModel";
		if (!config.dimension || config.dimension <= 1) {
			return "settings:cache.validation.dimension";
		}
	}
	if (config.ttl !== undefined && config.ttl < 0) return "settings:cache.validation.ttl";
	if (config.threshold !== undefined && (config.threshold < 0 || config.threshold > 1)) {
		return "settings:cache.validation.threshold";
	}
	if (
		config.conversation_history_threshold !== undefined &&
		(config.conversation_history_threshold < 1 || config.conversation_history_threshold > 50)
	) {
		return "settings:cache.validation.history";
	}
	return null;
};

export default function CachingView() {
	const { t } = useTranslation(["common", "settings"]);
	const { data: bifrostConfig, isLoading: configLoading, error: configError } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreEnabled = bifrostConfig?.is_cache_connected ?? false;

	// Local cache state lives on the plugin row keyed by SEMANTIC_CACHE_PLUGIN.
	// No dedicated /local-cache-config endpoint exists — the plugins API is
	// the source of truth for both the enabled flag and the config blob.
	const { data: plugins, isLoading: pluginsLoading } = useGetPluginsQuery();
	const semanticCachePlugin = useMemo(() => plugins?.find((p) => p.name === SEMANTIC_CACHE_PLUGIN), [plugins]);
	const enabledOnServer = Boolean(semanticCachePlugin?.enabled);

	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();
	const providers = useMemo(() => providersData || [], [providersData]);
	const embeddingProviders = useMemo(() => providers.filter(supportsEmbedding), [providers]);

	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const isSaving = isUpdating || isCreating;

	const [cacheConfig, setCacheConfig] = useState<EditorCacheConfig>(defaultDirectConfig);
	const [serverCacheConfig, setServerCacheConfig] = useState<EditorCacheConfig>(defaultDirectConfig);
	const [mode, setMode] = useState<CacheMode>("direct");

	// Hydrate from the plugin row once it lands. If the plugin doesn't exist
	// yet (first-time setup), keep the default direct-only seed so the user
	// can start typing before any save.
	useEffect(() => {
		if (plugins === undefined) return;
		if (!semanticCachePlugin?.config) return;
		const editorConfig = toEditorCacheConfig(semanticCachePlugin.config as Partial<EditorCacheConfig>);
		setCacheConfig(editorConfig);
		setServerCacheConfig(editorConfig);
		setMode(inferMode(editorConfig));
	}, [plugins, semanticCachePlugin]);

	useEffect(() => {
		if (providersError) {
			toast.error(t("settings:cache.providersLoadFailed", { message: getErrorMessage(providersError as any) }));
		}
	}, [t, providersError, t]);

	// Surface validation problems inline rather than only on Save click.
	const validationError = useMemo(() => validateForSave(cacheConfig, mode), [cacheConfig, mode]);

	// Only show the dimension/namespace heads-up when the user has actually
	// touched a structural field. Showing it permanently in semantic mode
	// trains users to ignore it; showing it on diff makes it land.
	const hasStructuralChange = useMemo(() => {
		return (
			cacheConfig.provider !== serverCacheConfig.provider ||
			cacheConfig.embedding_model !== serverCacheConfig.embedding_model ||
			cacheConfig.dimension !== serverCacheConfig.dimension
		);
	}, [cacheConfig, serverCacheConfig]);

	const hasUnsavedConfigChanges = useMemo(() => {
		const fields: (keyof EditorCacheConfig)[] = [
			"provider",
			"embedding_model",
			"dimension",
			"ttl",
			"threshold",
			"conversation_history_threshold",
			"exclude_system_prompt",
			"cache_by_model",
			"cache_by_provider",
			"disable_identity_scoping",
			"vector_store_namespace",
			"default_cache_key",
		];
		const changed = fields.some((k) => (cacheConfig[k] ?? "") !== (serverCacheConfig[k] ?? ""));
		const modeChanged = inferMode(serverCacheConfig) !== mode;
		return changed || modeChanged;
	}, [cacheConfig, serverCacheConfig, mode]);

	const updateLocal = (updates: Partial<EditorCacheConfig>) => {
		setCacheConfig((prev) => ({ ...prev, ...updates }));
	};

	// Toggle handler. Updates the semantic_cache plugin's enabled flag while
	// keeping the last-saved config so the backend can ReloadPlugin/RemovePlugin
	// based on the new flag. When toggling on for the first time and no plugin
	// row exists, we seed it with the current editor config (direct-only by
	// default) so the create call has a valid payload — the user can refine
	// the config and Save afterwards.
	const handleToggle = async (checked: boolean) => {
		try {
			if (semanticCachePlugin) {
				await updatePlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					data: { enabled: checked, config: semanticCachePlugin.config },
				}).unwrap();
			} else {
				// No plugin row + user toggling off ⇒ nothing to disable.
				// Bail before the success toast so we don't lie about the state.
				if (!checked) return;
				const err = validateForSave(cacheConfig, mode);
				if (err) {
					toast.error(t(err));
					return;
				}
				const payload = buildPayload(cacheConfig, mode);
				await createPlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					enabled: true,
					config: payload,
					path: "",
				}).unwrap();
			}
			toast.success(checked ? t("settings:cache.enabledToast") : t("settings:cache.disabledToast"));
		} catch (error) {
			toast.error(
				checked
					? t("settings:cache.enableFailed", { message: getErrorMessage(error) })
					: t("settings:cache.disableFailed", { message: getErrorMessage(error) }),
			);
		}
	};

	const handleSave = async () => {
		const err = validateForSave(cacheConfig, mode);
		if (err) {
			toast.error(t(err));
			return;
		}
		const payload = buildPayload(cacheConfig, mode);
		try {
			const updated = semanticCachePlugin
				? await updatePlugin({
						name: SEMANTIC_CACHE_PLUGIN,
						data: { enabled: semanticCachePlugin.enabled, config: payload },
					}).unwrap()
				: await createPlugin({
						name: SEMANTIC_CACHE_PLUGIN,
						enabled: false,
						config: payload,
						path: "",
					}).unwrap();
			const editor = toEditorCacheConfig(updated.config as Partial<EditorCacheConfig>);
			setCacheConfig(editor);
			setServerCacheConfig(editor);
			setMode(inferMode(editor));
			toast.success(t("settings:cache.saved"));
		} catch (error) {
			toast.error(t("settings:cache.saveFailed", { message: getErrorMessage(error) }));
		}
	};

	const cachingActive = enabledOnServer && isVectorStoreEnabled;
	const isLoading = configLoading || pluginsLoading;

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6">
			<PageTitle title={t("settings:cache.title")}>
				<Trans i18nKey="settings:cache.subtitle" t={t} components={{ b: <b /> }} />{" "}
				{!isVectorStoreEnabled && (
					<span className="text-destructive font-medium">
						<Trans i18nKey="settings:cache.requiresVectorStore" t={t} components={{ code: <code /> }} />
					</span>
				)}
			</PageTitle>

			{configError !== undefined && (
				<div className="border-destructive/50 bg-destructive/10 rounded-sm border p-4">
					<p className="text-destructive text-sm font-medium">{t("settings:cache.loadFailed")}</p>
					<p className="text-muted-foreground mt-1 text-sm">{getErrorMessage(configError) || t("settings:cache.unexpectedError")}</p>
				</div>
			)}

			{isLoading && (
				<div className="flex items-center justify-center py-8">
					<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
				</div>
			)}

			{!isLoading && !configError && (
				<div className="space-y-4">
					{/* Enable toggle flips plugin.enabled on the semantic_cache
					    plugin row. The plugins API handles ReloadPlugin /
					    RemovePlugin transparently on update. */}
					<div className="flex items-center justify-between space-x-2">
						<div className="space-y-0.5">
							<label htmlFor="enable-caching" className="text-sm font-medium">
								{t("settings:cache.enableCaching")}
							</label>
							<p className="text-muted-foreground text-sm">{t("settings:cache.enableCachingDescription")} </p>
						</div>
						<Tooltip>
							<TooltipTrigger asChild>
								<span tabIndex={!hasSettingsUpdateAccess ? 0 : undefined}>
									<Switch
										id="enable-caching"
										data-testid="caching-enable-switch"
										size="md"
										checked={cachingActive}
										disabled={!isVectorStoreEnabled || isSaving || !hasSettingsUpdateAccess}
										onCheckedChange={handleToggle}
									/>
								</span>
							</TooltipTrigger>
							{!hasSettingsUpdateAccess && <TooltipContent>{t("settings:shared.noPermission")}</TooltipContent>}
						</Tooltip>
					</div>

					{providersLoading ? (
						<div className="flex items-center justify-center py-4">
							<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
						</div>
					) : (
						<>
							<div className={cn("space-y-4", !cachingActive && "pointer-events-none opacity-50")} aria-disabled={!cachingActive}>
								{/* Mode picker. Direct-only is first-class. */}
								<div className="space-y-2">
									<Label className="text-sm font-medium">{t("settings:cache.cacheMode")}</Label>
									<Tabs value={mode} onValueChange={(v) => setMode(v as CacheMode)}>
										<TabsList className="flex w-full justify-start">
											{/* Panels live outside <Tabs>, so drop the dangling aria-controls Radix would add. */}
											<TabsTrigger value="direct" data-testid="caching-mode-direct-tab" aria-controls={undefined}>
												{t("settings:cache.modeDirect")}
											</TabsTrigger>
											<TabsTrigger
												value="semantic"
												data-testid="caching-mode-semantic-tab"
												aria-controls={undefined}
												disabled={embeddingProviders.length === 0}
												title={embeddingProviders.length === 0 ? t("settings:cache.semanticDisabledTitle") : undefined}
											>
												{t("settings:cache.modeSemantic")}
											</TabsTrigger>
										</TabsList>
									</Tabs>
									<p className="text-muted-foreground text-xs">
										{mode === "direct" ? t("settings:cache.modeDirectDesc") : t("settings:cache.modeSemanticDesc")}
									</p>
								</div>

								{validationError && (
									<div className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border p-3 text-xs">
										{validationError}
									</div>
								)}

								{/* Provider/model/dimension only appear in semantic mode. */}
								{mode === "semantic" && (
									<>
										{hasStructuralChange && (
											<div className="rounded-sm border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900">
												<Trans i18nKey="settings:cache.headsUp" t={t} components={{ b: <b />, em: <em /> }} />
											</div>
										)}

										<div className="space-y-4">
											<h3 className="text-sm font-medium">{t("settings:cache.embeddingSection")}</h3>
											<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
												<div className="space-y-2">
													<Label htmlFor="provider">{t("settings:cache.configuredProviders")}</Label>
													<ProviderSelector
														inputId="provider"
														data-testid="caching-provider-select"
														filter={supportsEmbedding}
														value={cacheConfig.provider ?? ""}
														onChange={(value: string) =>
															updateLocal({
																provider: value as ModelProviderName,
																embedding_model: value === cacheConfig.provider ? cacheConfig.embedding_model : "",
															})
														}
													/>
												</div>
												<div className="space-y-2">
													<Label htmlFor="embedding_model">{t("settings:cache.embeddingModel")}</Label>
													<ModelSelector
														inputId="embedding_model"
														data-testid="caching-embedding-model-select"
														provider={cacheConfig.provider || undefined}
														value={cacheConfig.embedding_model ?? ""}
														onChange={(model) => updateLocal({ embedding_model: model })}
														allowCustomModel
														placeholder={
															cacheConfig.provider ? t("settings:cache.modelPlaceholder") : t("settings:cache.modelPlaceholderNoProvider")
														}
														disabled={!cacheConfig.provider}
													/>
												</div>
											</div>
											<p className="text-muted-foreground text-xs">{t("settings:cache.apiKeyNote")}</p>
											<div className="space-y-2">
												<Label htmlFor="dimension">{t("settings:cache.dimension")}</Label>
												<Input
													id="dimension"
													data-testid="caching-dimension-input"
													type="number"
													min="2"
													value={cacheConfig.dimension === undefined || Number.isNaN(cacheConfig.dimension) ? "" : cacheConfig.dimension}
													onChange={(e) => {
														const value = e.target.value;
														if (value === "") {
															updateLocal({ dimension: undefined });
															return;
														}
														const parsed = parseInt(value);
														if (!Number.isNaN(parsed)) {
															updateLocal({ dimension: parsed });
														}
													}}
												/>
												<p className="text-muted-foreground text-xs">
													<Trans i18nKey="settings:cache.dimensionHint" t={t} components={{ code: <code /> }} />
												</p>
											</div>
										</div>
									</>
								)}

								{/* Cache settings shared across modes. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">{t("settings:cache.cacheSettings")}</h3>
									<div className={cn("grid gap-4", mode === "semantic" ? "grid-cols-1 md:grid-cols-2" : "grid-cols-1")}>
										<div className="space-y-2">
											<Label htmlFor="ttl">{t("settings:cache.ttl")}</Label>
											<Input
												id="ttl"
												data-testid="caching-ttl-input"
												type="number"
												min="1"
												value={cacheConfig.ttl === undefined || Number.isNaN(cacheConfig.ttl) ? "" : cacheConfig.ttl}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														updateLocal({ ttl: undefined });
														return;
													}
													const parsed = parseInt(value);
													if (!Number.isNaN(parsed)) {
														updateLocal({ ttl: parsed });
													}
												}}
											/>
											<p className="text-muted-foreground text-xs">
												<Trans i18nKey="settings:cache.ttlHint" t={t} components={{ b: <b /> }} />
											</p>
										</div>
										{mode === "semantic" && (
											<div className="space-y-2">
												<Label htmlFor="threshold">{t("settings:cache.threshold")}</Label>
												<Input
													id="threshold"
													data-testid="caching-threshold-input"
													type="number"
													min="0"
													max="1"
													step="0.01"
													value={cacheConfig.threshold === undefined || Number.isNaN(cacheConfig.threshold) ? "" : cacheConfig.threshold}
													onChange={(e) => {
														const value = e.target.value;
														if (value === "") {
															updateLocal({ threshold: undefined });
															return;
														}
														const parsed = parseFloat(value);
														if (!Number.isNaN(parsed)) {
															updateLocal({ threshold: parsed });
														}
													}}
												/>
												<p className="text-muted-foreground text-xs">
													<Trans i18nKey="settings:cache.thresholdHint" t={t} components={{ b: <b /> }} />
												</p>
											</div>
										)}
									</div>
								</div>

								{/* Storage & Cache Key. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">{t("settings:cache.storageSection")}</h3>
									<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
										<div className="space-y-2">
											<Label htmlFor="vector_store_namespace">{t("settings:cache.vectorNamespace")}</Label>
											<Input
												id="vector_store_namespace"
												data-testid="caching-vector-store-namespace-input"
												type="text"
												placeholder={t("settings:cache.nsPlaceholder")}
												value={cacheConfig.vector_store_namespace ?? ""}
												onChange={(e) => updateLocal({ vector_store_namespace: e.target.value })}
											/>
											<p className="text-muted-foreground text-xs">
												<Trans i18nKey="settings:cache.nsHint" t={t} components={{ code: <code /> }} />
											</p>
										</div>
										<div className="space-y-2">
											<Label htmlFor="default_cache_key">{t("settings:cache.defaultCacheKey")}</Label>
											<Input
												id="default_cache_key"
												data-testid="caching-default-cache-key-input"
												type="text"
												placeholder={t("settings:cache.ckPlaceholder")}
												value={cacheConfig.default_cache_key ?? ""}
												onChange={(e) => updateLocal({ default_cache_key: e.target.value })}
											/>
											<p className="text-muted-foreground text-xs">
												<Trans i18nKey="settings:cache.ckHint" t={t} components={{ b: <b /> }} />
											</p>
										</div>
									</div>
								</div>

								{/* Conversation Settings. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">{t("settings:cache.conversationSection")}</h3>
									<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
										<div className="space-y-2">
											<Label htmlFor="conversation_history_threshold">{t("settings:cache.historyThreshold")}</Label>
											<Input
												id="conversation_history_threshold"
												data-testid="caching-conversation-history-threshold-input"
												type="number"
												min="1"
												max="50"
												value={
													cacheConfig.conversation_history_threshold === undefined ||
													Number.isNaN(cacheConfig.conversation_history_threshold)
														? ""
														: cacheConfig.conversation_history_threshold
												}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														updateLocal({ conversation_history_threshold: undefined });
														return;
													}
													const parsed = parseInt(value);
													if (!Number.isNaN(parsed)) {
														updateLocal({ conversation_history_threshold: parsed });
													}
												}}
											/>
											<p className="text-muted-foreground text-xs">{t("settings:cache.historyHint")}</p>
										</div>
									</div>
									<div className="space-y-2">
										<div className="flex h-fit items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">{t("settings:cache.excludeSystem")}</Label>
												<p className="text-muted-foreground text-xs">{t("settings:cache.excludeSystemHint")}</p>
											</div>
											<Switch
												aria-label={t("settings:cache.excludeSystemAria")}
												data-testid="caching-exclude-system-prompt-switch"
												checked={cacheConfig.exclude_system_prompt || false}
												onCheckedChange={(checked) => updateLocal({ exclude_system_prompt: checked })}
												size="md"
											/>
										</div>
									</div>
								</div>

								{/* Cache Behavior applies to both modes. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">{t("settings:cache.keyComposition")}</h3>
									<div className="space-y-3">
										<div className="flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">{t("settings:cache.cacheByModel")}</Label>
												<p className="text-muted-foreground text-xs">{t("settings:cache.cacheByModelHint")}</p>
											</div>
											<Switch
												aria-label={t("settings:cache.cacheByModelAria")}
												data-testid="caching-cache-by-model-switch"
												checked={cacheConfig.cache_by_model}
												onCheckedChange={(checked) => updateLocal({ cache_by_model: checked })}
												size="md"
											/>
										</div>
										<div className="flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">{t("settings:cache.cacheByProvider")}</Label>
												<p className="text-muted-foreground text-xs">{t("settings:cache.cacheByProviderHint")}</p>
											</div>
											<Switch
												aria-label={t("settings:cache.cacheByProviderAria")}
												data-testid="caching-cache-by-provider-switch"
												checked={cacheConfig.cache_by_provider}
												onCheckedChange={(checked) => updateLocal({ cache_by_provider: checked })}
												size="md"
											/>
										</div>
										<div className="flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">{t("settings:cache.scopeIdentity")}</Label>
												<p className="text-muted-foreground text-xs">{t("settings:cache.scopeIdentityHint")}</p>
											</div>
											<Switch
												aria-label={t("settings:cache.scopeIdentityAria")}
												data-testid="caching-identity-scoping-switch"
												checked={!cacheConfig.disable_identity_scoping}
												onCheckedChange={(checked) => updateLocal({ disable_identity_scoping: !checked })}
												size="md"
											/>
										</div>
									</div>
								</div>

								<div className="space-y-2">
									<Label className="text-sm font-medium">{t("settings:cache.perRequest")}</Label>
									<ul className="text-muted-foreground list-inside list-disc text-xs">
										<li>
											<Trans i18nKey="settings:cache.overrideKey" t={t} components={{ b: <b /> }} />
										</li>
										<li>
											<Trans i18nKey="settings:cache.overrideTtl" t={t} components={{ b: <b /> }} />
										</li>
										<li>
											<Trans i18nKey="settings:cache.overrideThreshold" t={t} components={{ b: <b /> }} />
										</li>
										<li>
											<Trans i18nKey="settings:cache.overrideType" t={t} components={{ b: <b />, code: <code /> }} />
										</li>
										<li>
											<Trans i18nKey="settings:cache.overrideNoStore" t={t} components={{ b: <b />, code: <code /> }} />
										</li>
									</ul>
								</div>
							</div>

							<div className="bg-card sticky bottom-0 flex justify-end py-2">
								<Tooltip>
									<TooltipTrigger asChild>
										<span tabIndex={!hasSettingsUpdateAccess ? 0 : undefined}>
											<Button
												data-testid="caching-save-button"
												onClick={handleSave}
												disabled={!hasUnsavedConfigChanges || isSaving || Boolean(validationError) || !hasSettingsUpdateAccess}
											>
												{isSaving ? t("common:state.saving") : t("settings:shared.saveChanges")}
											</Button>
										</span>
									</TooltipTrigger>
									{!hasSettingsUpdateAccess && <TooltipContent>{t("settings:shared.noPermission")}</TooltipContent>}
								</Tooltip>
							</div>
						</>
					)}
				</div>
			)}
		</div>
	);
}