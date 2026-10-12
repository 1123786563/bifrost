import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Trans, useTranslation } from "react-i18next";

// Mirrors the backend Slugify: lowercase, keep [a-z0-9], collapse other runs to a single
// hyphen, trim leading/trailing hyphens. Keep in sync with framework/configstore Slugify.
export function slugify(value: string): string {
	let out = "";
	let prevHyphen = false;
	for (const ch of value.toLowerCase().trim()) {
		if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) {
			out += ch;
			prevHyphen = false;
		} else if (!prevHyphen) {
			out += "-";
			prevHyphen = true;
		}
	}
	return out.replace(/^-+|-+$/g, "");
}

interface VirtualMCPGeneralTabProps {
	name: string;
	setName: (value: string) => void;
	endpointSlug: string;
	setEndpointSlug: (value: string) => void;
	description: string;
	setDescription: (value: string) => void;
	enabled: boolean;
	setEnabled: (value: boolean) => void;
	isCreate: boolean;
}

export default function VirtualMCPGeneralTab({
	name,
	setName,
	endpointSlug,
	setEndpointSlug,
	description,
	setDescription,
	enabled,
	setEnabled,
	isCreate,
}: VirtualMCPGeneralTabProps) {
	const { t } = useTranslation(["common", "mcp"]);
	// The endpoint the server will actually serve: the typed slug if present, else derived from the name.
	const previewSlug = slugify(endpointSlug.trim() || name);
	return (
		<div className="flex flex-col gap-5">
			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-name">{t("common:fields.name")}</Label>
				<Input
					id="vmcp-name"
					value={name}
					onChange={(e) => setName(e.target.value)}
					placeholder={t("mcp:virtualMcps.general.namePlaceholder")}
					data-testid="virtual-mcp-name-input"
				/>
			</div>

			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-slug">{t("mcp:virtualMcps.general.slugLabel")}</Label>
				<Input
					id="vmcp-slug"
					value={endpointSlug}
					onChange={(e) => setEndpointSlug(e.target.value)}
					placeholder={t("mcp:virtualMcps.general.slugPlaceholder")}
					disabled={!isCreate}
					className="font-mono"
					data-testid="virtual-mcp-slug-input"
				/>
				<p className="text-muted-foreground text-xs">
					{isCreate ? t("mcp:virtualMcps.general.slugCreateHint") : t("mcp:virtualMcps.general.slugEditHint")}
				</p>
				{isCreate && previewSlug && (
					<p className="text-muted-foreground text-xs" data-testid="virtual-mcp-slug-preview">
						<Trans
							t={t}
							i18nKey="mcp:virtualMcps.general.servedAt"
							values={{ endpoint: `/mcp/${previewSlug}` }}
							components={[<span key="0" className="text-foreground font-mono" />]}
						>
							Served at <span className="text-foreground font-mono">{"{{endpoint}}"}</span>
						</Trans>
					</p>
				)}
			</div>

			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-description">{t("common:fields.description")}</Label>
				<Textarea
					id="vmcp-description"
					value={description}
					onChange={(e) => setDescription(e.target.value)}
					placeholder={t("mcp:virtualMcps.general.descriptionPlaceholder")}
					rows={3}
					data-testid="virtual-mcp-description-input"
				/>
			</div>

			<div className="flex items-center justify-between rounded-md border p-3">
				<div className="flex flex-col gap-0.5">
					<Label htmlFor="vmcp-enabled">{t("common:state.enabled")}</Label>
					<p className="text-muted-foreground text-xs">{t("mcp:virtualMcps.general.enabledHint")}</p>
				</div>
				<Switch id="vmcp-enabled" checked={enabled} onCheckedChange={setEnabled} data-testid="virtual-mcp-enabled-switch" />
			</div>
		</div>
	);
}