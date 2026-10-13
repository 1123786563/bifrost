import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useCreateWebhookEndpointMutation, useUpdateWebhookEndpointMutation } from "@/lib/store";
import { SecretVar } from "@/lib/types/schemas";
import { WEBHOOK_EVENTS, WEBHOOK_TUNING_DEFAULTS, WebhookEndpoint, WebhookEndpointRequest, WebhookEvent } from "@/lib/types/webhooks";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { WebhookSecretReveal } from "../dialogs/webhookSecretDialog";

const webhookFormSchema = z
	.object({
		name: z.string().trim().min(1, "Name is required"),
		url: z
			.string()
			.trim()
			.min(1, "URL is required")
			.refine((value) => {
				try {
					const parsed = new URL(value);
					return ["http:", "https:"].includes(parsed.protocol) && Boolean(parsed.hostname);
				} catch {
					return false;
				}
			}, "Enter a valid HTTP(S) URL"),
		events: z.array(z.enum(["async_job.completed", "async_job.failed"])).min(1, "Subscribe to at least one event"),
		include_response: z.boolean(),
		allow_private_network: z.boolean(),
		max_retries: z.number().int().min(0).optional(),
		retry_backoff_initial_seconds: z.number().int().min(0).optional(),
		retry_backoff_max_seconds: z.number().int().min(0).optional(),
		attempt_timeout_seconds: z.number().int().min(0).optional(),
		max_response_payload_kbs: z.number().int().min(0).optional(),
		max_concurrent_deliveries: z.number().int().min(0).optional(),
	})
	.refine((data) => data.allow_private_network || !data.url.startsWith("http://"), {
		message: 'http:// URLs require "Allow private network" to be enabled',
		path: ["url"],
	});

type WebhookFormData = z.infer<typeof webhookFormSchema>;

const TUNING_FIELD_KEYS: (keyof typeof WEBHOOK_TUNING_DEFAULTS)[] = [
	"max_retries",
	"retry_backoff_initial_seconds",
	"retry_backoff_max_seconds",
	"attempt_timeout_seconds",
	"max_response_payload_kbs",
	"max_concurrent_deliveries",
];

const formDefaults = (endpoint: WebhookEndpoint | null): WebhookFormData => ({
	name: endpoint?.name ?? "",
	url: endpoint?.url ?? "",
	events: endpoint?.events ?? ["async_job.completed", "async_job.failed"],
	include_response: endpoint?.include_response ?? false,
	allow_private_network: endpoint?.allow_private_network ?? false,
	max_retries: endpoint?.max_retries || undefined,
	retry_backoff_initial_seconds: endpoint?.retry_backoff_initial_seconds || undefined,
	retry_backoff_max_seconds: endpoint?.retry_backoff_max_seconds || undefined,
	attempt_timeout_seconds: endpoint?.attempt_timeout_seconds || undefined,
	max_response_payload_kbs: endpoint?.max_response_payload_kbs || undefined,
	max_concurrent_deliveries: endpoint?.max_concurrent_deliveries || undefined,
});

interface WebhookSheetProps {
	open: boolean;
	// null creates a new endpoint; otherwise edits the given one.
	endpoint: WebhookEndpoint | null;
	onClose: () => void;
	// Create returns the signing secret exactly once; the parent shows it.
	onSecret: (reveal: WebhookSecretReveal) => void;
}

export function WebhookSheet({ open, endpoint, onClose, onSecret }: WebhookSheetProps) {
	const { t } = useTranslation(["common", "governance"]);
	const isEditing = !!endpoint;
	const [createWebhookEndpoint, { isLoading: isCreating }] = useCreateWebhookEndpointMutation();
	const [updateWebhookEndpoint, { isLoading: isUpdating }] = useUpdateWebhookEndpointMutation();
	const isSaving = isCreating || isUpdating;

	// Header values are SecretVars managed outside the form (HeadersTable is
	// not a form control). Stored values arrive fully redacted; untouched
	// placeholders round-trip as-is and the server restores the real value.
	const [headers, setHeaders] = useState<Record<string, SecretVar>>({});
	// HeadersTable edits bypass react-hook-form, so header changes carry
	// their own dirty flag for the submit gate.
	const [headersDirty, setHeadersDirty] = useState(false);
	// Header validation runs on submit (HeadersTable is not a form control);
	// the message persists beside the table and clears when headers change.
	const [headersError, setHeadersError] = useState<string | null>(null);

	const {
		register,
		handleSubmit,
		setValue,
		watch,
		reset,
		formState: { errors, isDirty },
	} = useForm<WebhookFormData>({
		resolver: zodResolver(webhookFormSchema),
		defaultValues: formDefaults(null),
	});

	const events = watch("events");
	const includeResponse = watch("include_response");
	const allowPrivateNetwork = watch("allow_private_network");
	const hasChanges = isDirty || headersDirty;

	useEffect(() => {
		if (!open) return;
		reset(formDefaults(endpoint));
		setHeaders(endpoint?.headers ?? {});
		setHeadersDirty(false);
		setHeadersError(null);
	}, [open, endpoint, reset]);

	const handleHeadersChange = (next: Record<string, SecretVar>) => {
		setHeaders(next);
		setHeadersDirty(true);
		setHeadersError(null);
	};

	const toggleEvent = (event: WebhookEvent, checked: boolean) => {
		const next = checked ? [...events, event] : events.filter((e) => e !== event);
		setValue("events", next, { shouldValidate: true, shouldDirty: true });
	};

	const onSubmit = async (data: WebhookFormData) => {
		const cleanedHeaders: Record<string, SecretVar> = {};
		for (const [name, value] of Object.entries(headers)) {
			if (!name.trim()) continue;
			if (!value?.value?.trim() && !value?.ref?.trim()) {
				setHeadersError(`Header "${name}" must have a value`);
				return;
			}
			cleanedHeaders[name.trim()] = value;
		}

		const request: WebhookEndpointRequest = {
			name: data.name,
			url: data.url,
			events: data.events,
			headers: cleanedHeaders,
			include_response: data.include_response,
			allow_private_network: data.allow_private_network,
			disabled: endpoint?.disabled ?? false,
			max_retries: data.max_retries ?? 0,
			retry_backoff_initial_seconds: data.retry_backoff_initial_seconds ?? 0,
			retry_backoff_max_seconds: data.retry_backoff_max_seconds ?? 0,
			attempt_timeout_seconds: data.attempt_timeout_seconds ?? 0,
			max_response_payload_kbs: data.max_response_payload_kbs ?? 0,
			max_concurrent_deliveries: data.max_concurrent_deliveries ?? 0,
		};

		try {
			if (isEditing) {
				await updateWebhookEndpoint({ id: endpoint.id, data: request }).unwrap();
				toast.success(t("governance:webhooks.sheet.toastUpdated"));
				onClose();
			} else {
				const response = await createWebhookEndpoint(request).unwrap();
				toast.success(t("governance:webhooks.sheet.toastCreated"));
				onClose();
				onSecret({ endpointName: response.endpoint.name, secret: response.secret });
			}
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden px-0" data-testid="webhook-sheet-content">
				<SheetHeader className="flex flex-col items-start pt-8" headerClassName="px-4 md:px-6">
					<SheetTitle>{isEditing ? endpoint.name : t("governance:webhooks.sheet.createTitle")}</SheetTitle>
					<SheetDescription>
						{isEditing ? t("governance:webhooks.sheet.editDescription") : t("governance:webhooks.sheet.createDescription")}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
					<div className="flex-1 space-y-4 overflow-y-auto px-4 md:px-8">
						<div className="space-y-2">
							<Label htmlFor="webhook-name">{t("governance:webhooks.sheet.nameLabel")}</Label>
							<Input
								id="webhook-name"
								placeholder={t("governance:webhooks.sheet.namePlaceholder")}
								data-testid="webhook-name-input"
								{...register("name")}
								className={errors.name ? "border-destructive" : ""}
							/>
							{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						</div>

						<div className="space-y-2">
							<Label htmlFor="webhook-url">{t("governance:webhooks.sheet.urlLabel")}</Label>
							<Input
								id="webhook-url"
								placeholder={t("governance:webhooks.sheet.urlPlaceholder")}
								data-testid="webhook-url-input"
								{...register("url")}
								className={errors.url ? "border-destructive" : ""}
							/>
							{errors.url && <p className="text-destructive text-sm">{errors.url.message}</p>}
						</div>

						<div className="space-y-2">
							<Label>{t("governance:webhooks.sheet.eventsLabel")}</Label>
							<div className="space-y-2 rounded-sm border p-4">
								{WEBHOOK_EVENTS.map((event) => (
									<div key={event.value}>
										<label className="flex cursor-pointer items-center gap-2 text-sm" htmlFor={`webhook-event-${event.value}`}>
											<Checkbox
												id={`webhook-event-${event.value}`}
												checked={events.includes(event.value)}
												onCheckedChange={(checked) => toggleEvent(event.value, checked === true)}
												data-testid={`webhook-event-${event.value}-checkbox`}
											/>
											{event.label}
										</label>
										<p className="text-muted-foreground pl-6 text-xs">{event.description}</p>
									</div>
								))}
							</div>
							{errors.events && <p className="text-destructive text-sm">{errors.events.message}</p>}
						</div>

						<div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
							<div className="space-y-0.5">
								<Label htmlFor="webhook-include-response" className="text-sm font-normal">
									{t("governance:webhooks.sheet.includeResponse")}
								</Label>
								<p className="text-muted-foreground text-xs">{t("governance:webhooks.sheet.includeResponseHelp")}</p>
							</div>
							<Switch
								id="webhook-include-response"
								checked={includeResponse}
								onCheckedChange={(checked) => setValue("include_response", checked, { shouldDirty: true })}
								data-testid="webhook-include-response-switch"
							/>
						</div>

						<div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
							<div className="space-y-0.5">
								<Label htmlFor="webhook-private-network" className="text-sm font-normal">
									{t("governance:webhooks.sheet.allowPrivateNetwork")}
								</Label>
								<p className="text-muted-foreground text-xs">{t("governance:webhooks.sheet.allowPrivateNetworkHelp")}</p>
							</div>
							<Switch
								id="webhook-private-network"
								checked={allowPrivateNetwork}
								onCheckedChange={(checked) => setValue("allow_private_network", checked, { shouldDirty: true, shouldValidate: true })}
								data-testid="webhook-private-network-switch"
							/>
						</div>

						<div className="space-y-2">
							<HeadersTable<SecretVar>
								value={headers}
								onChange={handleHeadersChange}
								useSecretVarInput
								label={t("governance:webhooks.sheet.customHeaders")}
								keyPlaceholder={t("governance:webhooks.sheet.headersKeyPlaceholder")}
								valuePlaceholder={t("governance:webhooks.sheet.headersValuePlaceholder")}
							/>
							{headersError && (
								<p className="text-destructive text-xs" data-testid="webhook-headers-error">
									{headersError}
								</p>
							)}
							<p className="text-muted-foreground text-xs">{t("governance:webhooks.sheet.headersHelp")}</p>
						</div>

						<div className="space-y-4">
							<h3 className="text-sm leading-none font-medium" data-testid="webhook-tuning-heading">
								{t("governance:webhooks.sheet.deliveryTuning")}
							</h3>
							{TUNING_FIELD_KEYS.map((fieldKey) => {
								const fieldValue = watch(fieldKey);
								const isUsingDefault = fieldValue === undefined || fieldValue === 0;
								return (
									<div key={fieldKey} className="flex items-center justify-between gap-4 rounded-lg border px-4 py-2">
										<div className="flex flex-col items-start gap-0.5">
											<Label htmlFor={`webhook-${fieldKey}`} className="text-sm font-normal">
												{t(`governance:webhooks.sheet.tuning.${fieldKey}.label`)}
											</Label>
											<p className="text-muted-foreground text-xs">{t(`governance:webhooks.sheet.tuning.${fieldKey}.description`)}</p>
											{isUsingDefault && <p className="text-muted-foreground text-xs">{t("governance:webhooks.sheet.usingDefault")}</p>}
										</div>
										<Input
											id={`webhook-${fieldKey}`}
											type="number"
											min="0"
											className={`w-24 ${isUsingDefault ? "text-muted-foreground" : ""}`}
											placeholder={String(WEBHOOK_TUNING_DEFAULTS[fieldKey])}
											data-testid={`webhook-${fieldKey}-input`}
											value={fieldValue ?? ""}
											onChange={(e) =>
												setValue(fieldKey, e.target.value === "" ? undefined : Number(e.target.value), {
													shouldValidate: true,
													shouldDirty: true,
												})
											}
										/>
									</div>
								);
							})}
							<p className="text-muted-foreground pb-2 text-xs">{t("governance:webhooks.sheet.tuningTtlNote")}</p>
						</div>
					</div>

					<div className="dark:bg-card border-border border-t bg-white px-4 py-4 md:px-8">
						<div className="flex justify-end gap-2">
							<Button type="button" variant="outline" onClick={onClose} disabled={isSaving} data-testid="webhook-cancel-btn">
								{t("common:actions.cancel")}
							</Button>
							<Button type="submit" disabled={isSaving || !hasChanges} data-testid="webhook-save-btn">
								{isSaving
									? t("governance:webhooks.sheet.saveButton")
									: isEditing
										? t("governance:webhooks.sheet.updateButton")
										: t("governance:webhooks.sheet.createButton")}
							</Button>
						</div>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}