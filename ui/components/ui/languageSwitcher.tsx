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