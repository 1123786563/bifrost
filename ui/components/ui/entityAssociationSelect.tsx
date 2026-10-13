import { useCallback, useMemo } from "react";
import { AsyncMultiSelect } from "./asyncMultiselect";
import { Option } from "./multiselectUtils";
import { cn } from "./utils";
import { useTranslation } from "react-i18next";

// Entity types supported by this component
export type EntityType = "virtualKey" | "team" | "customer" | "user" | "provider" | "apiKey";

// Generic entity option
export interface EntityOption {
	id: string | number;
	label: string;
	description?: string;
	metadata?: Record<string, unknown>;
}

// Meta type for AsyncMultiSelect
interface EntityOptionMeta {
	id: string | number;
	description?: string;
	metadata?: Record<string, unknown>;
}

interface EntityAssociationSelectProps {
	/** The type of entity being selected */
	entityType: EntityType;
	/** Currently selected entity IDs */
	value: (string | number)[];
	/** Callback when selection changes */
	onChange: (ids: (string | number)[]) => void;
	/** Placeholder text */
	placeholder?: string;
	/** Whether the component is disabled */
	disabled?: boolean;
	/** Additional CSS classes */
	className?: string;
	/**
	 * Custom reload function for fetching options.
	 * If provided, this will be used instead of the default behavior.
	 * The function should call the callback with filtered options.
	 */
	customReload?: (query: string, callback: (options: Option<EntityOptionMeta>[]) => void) => void;
	/**
	 * Static options to use when customReload is not provided.
	 * This is useful for simple use cases where all options are already available.
	 */
	options?: EntityOption[];
	/**
	 * Whether to allow creating new options
	 */
	isCreatable?: boolean;
	/**
	 * Callback when a new option is created
	 */
	onCreateOption?: (value: string) => void;
	/**
	 * Format function for creating new option labels
	 */
	formatCreateLabel?: (inputValue: string) => string;
	/**
	 * Message to display when no options are available
	 */
	noOptionsMessage?: () => string;
}

// Default placeholder text for each entity type
const defaultPlaceholderKeys: Record<EntityType, string> = {
	virtualKey: "workspaces:entitySelect.add.virtualKey",
	team: "workspaces:entitySelect.add.team",
	customer: "workspaces:entitySelect.add.customer",
	user: "workspaces:entitySelect.add.user",
	provider: "workspaces:entitySelect.add.provider",
	apiKey: "workspaces:entitySelect.add.apiKey",
};

// Default no options messages for each entity type
const defaultNoOptionsKeys: Record<EntityType, string> = {
	virtualKey: "workspaces:entitySelect.none.virtualKey",
	team: "workspaces:entitySelect.none.team",
	customer: "workspaces:entitySelect.none.customer",
	user: "workspaces:entitySelect.none.user",
	provider: "workspaces:entitySelect.none.provider",
	apiKey: "workspaces:entitySelect.none.apiKey",
};

// Label translation keys for each entity type; resolve with t() at the usage site
export const entityTypeLabels: Record<EntityType, string> = {
	virtualKey: "workspaces:filters.sections.virtualKeys",
	team: "workspaces:filters.sections.teams",
	customer: "workspaces:filters.sections.customers",
	user: "workspaces:filters.sections.user",
	provider: "workspaces:filters.sections.providers",
	apiKey: "workspaces:promptsRepo.apiKey.apiKeysGroup",
};

export function EntityAssociationSelect({
	entityType,
	value,
	onChange,
	placeholder,
	disabled = false,
	className,
	customReload,
	options = [],
	isCreatable = false,
	onCreateOption,
	formatCreateLabel,
	noOptionsMessage,
}: EntityAssociationSelectProps) {
	const { t } = useTranslation(["common", "workspaces"]);
	// Convert static options to AsyncMultiSelect format using meta for complex data
	const defaultOptions = useMemo((): Option<EntityOptionMeta>[] => {
		return options.map((opt) => ({
			label: opt.label,
			value: String(opt.id),
			meta: {
				id: opt.id,
				description: opt.description,
				metadata: opt.metadata,
			},
		}));
	}, [options]);

	// Convert selected IDs to Option format
	const selectedValues = useMemo((): Option<EntityOptionMeta>[] => {
		return value.map((id) => {
			// Try to find the option in the provided options
			const existingOption = options.find((opt) => opt.id === id);
			if (existingOption) {
				return {
					label: existingOption.label,
					value: String(existingOption.id),
					meta: {
						id: existingOption.id,
						description: existingOption.description,
						metadata: existingOption.metadata,
					},
				};
			}
			// If not found, create a basic option
			return {
				label: String(id),
				value: String(id),
				meta: {
					id,
				},
			};
		});
	}, [value, options]);

	// Filter options based on query
	const filterOptions = useCallback(
		(query: string, callback: (options: Option<EntityOptionMeta>[]) => void) => {
			const lowerQuery = query.toLowerCase();
			const filtered = defaultOptions.filter(
				(opt) => opt.label.toLowerCase().includes(lowerQuery) || opt.meta?.description?.toLowerCase().includes(lowerQuery),
			);
			callback(filtered);
		},
		[defaultOptions],
	);

	// Use custom reload if provided, otherwise use local filter
	const reload = customReload || filterOptions;

	// Handle selection change
	const handleChange = useCallback(
		(selected: Option<EntityOptionMeta>[]) => {
			const ids = selected.map((opt) => opt.meta?.id ?? opt.value);
			onChange(ids);
		},
		[onChange],
	);

	// Handle creating new option
	const handleCreateOption = useCallback(
		(inputValue: string) => {
			if (onCreateOption) {
				onCreateOption(inputValue);
			}
		},
		[onCreateOption],
	);

	return (
		<div className={cn("w-full", className)}>
			<AsyncMultiSelect<EntityOptionMeta>
				placeholder={placeholder || t(defaultPlaceholderKeys[entityType])}
				disabled={disabled}
				defaultOptions={defaultOptions}
				reload={reload}
				debounce={200}
				onChange={handleChange}
				value={selectedValues}
				isClearable
				closeMenuOnSelect={false}
				hideSelectedOptions={false}
				isCreatable={isCreatable}
				onCreateOption={handleCreateOption}
				formatCreateLabel={formatCreateLabel || ((value) => t("workspaces:entitySelect.addQuoted", { value }))}
				noOptionsMessage={noOptionsMessage || (() => t(defaultNoOptionsKeys[entityType]))}
				views={{
					option: (props) => {
						// Access data as Option<EntityOptionMeta> since that's the actual runtime type
						const data = props.data as unknown as Option<EntityOptionMeta>;
						return (
							<div
								className={cn(
									"flex w-full cursor-pointer flex-col gap-0.5 rounded-sm p-2 text-sm",
									props.isFocused && "bg-background-highlight-primary/60",
									props.isSelected && "bg-background-highlight-primary/40",
								)}
								role="presentation"
								onClick={() => props.selectOption(props.data)}
							>
								<div className="flex items-center justify-between">
									<span className="text-content-primary font-medium">{data.label}</span>
									{props.isSelected && <span className="text-primary text-xs">{t("common:fields.selected")}</span>}
								</div>
								{data.meta?.description && <span className="text-content-tertiary line-clamp-1 text-xs">{data.meta.description}</span>}
							</div>
						);
					},
				}}
			/>
		</div>
	);
}

/**
 * Helper function to create EntityOption from simple ID list
 * Useful when you just have IDs without full entity info
 */
export function createOptionsFromIds(ids: (string | number)[]): EntityOption[] {
	return ids.map((id) => ({
		id,
		label: String(id),
	}));
}

/**
 * Helper function to create EntityOption with label mapping
 */
export function createOptionsWithLabels(
	items: { id: string | number; name?: string; label?: string; description?: string }[],
): EntityOption[] {
	return items.map((item) => ({
		id: item.id,
		label: item.name || item.label || String(item.id),
		description: item.description,
	}));
}