import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { toast } from "sonner";
import {
	getErrorCode,
	getErrorMessage,
	useGetOAuth2ConsentFlowQuery,
	useIsAuthEnabledQuery,
	useSubmitOAuth2ConsentFlowMutation,
} from "@/lib/store";
import { getActiveTempToken } from "@/lib/store/apis/tempToken";
import type { TFunction } from "i18next";
import { Fingerprint, KeyRound, Loader2, LogIn, ShieldCheck, UserRound } from "lucide-react";
import { useQueryState } from "nuqs";
import React, { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

// Mirrors handlers.TempTokenRejectedCode: a presented token was refused.
const TEMP_TOKEN_REJECTED_CODE = "temp_token_rejected";

export default function OAuth2ConsentPage() {
	const { t } = useTranslation(["common", "auth"]);
	const [flowId] = useQueryState("flow");

	if (!flowId) {
		return (
			<Shell>
				<div className="text-center">
					<h1 className="text-xl font-semibold">{t("auth:consent.missingFlow")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">
						{t("auth:consent.missingFlowDesc1")} <code className="bg-muted rounded px-1 py-0.5 text-xs">flow</code>{" "}
						{t("auth:consent.missingFlowDesc2")}
					</p>
				</div>
			</Shell>
		);
	}

	return <ConsentView flowId={flowId} />;
}

// isSafeRedirect rejects URLs whose scheme could execute script when assigned to
// location.href (javascript:, data:, …) while allowing http(s) and any native
// custom-scheme redirect a client may have registered.
function isSafeRedirect(url: string): boolean {
	try {
		const proto = new URL(url, window.location.origin).protocol.toLowerCase();
		return !["javascript:", "data:", "vbscript:", "blob:", "file:"].includes(proto);
	} catch {
		return false;
	}
}

function ConsentView({ flowId }: { flowId: string }) {
	const { t } = useTranslation(["common", "auth"]);
	const { data: flow, isLoading, isError, error } = useGetOAuth2ConsentFlowQuery(flowId);
	const { data: authState } = useIsAuthEnabledQuery();
	const [submitFlow, { isLoading: submitting }] = useSubmitOAuth2ConsentFlowMutation();
	const [vkValue, setVkValue] = useState("");
	const [selectedMode, setSelectedMode] = useState<"vk" | "session" | "user" | null>(null);

	// TempTokenScope installs the token during its own render, so any
	// sessionStorage restore has already happened by now.
	const [usingTempToken] = useState(() => getActiveTempToken() !== null);

	const loginHref = useMemo(() => {
		const returnPath = `/oauth/consent?flow=${encodeURIComponent(flowId)}`;
		return `/login?goto=${encodeURIComponent(returnPath)}`;
	}, [flowId]);

	const signedOut = authState?.is_auth_enabled === true && authState.has_valid_token === false;
	const showLoginOption = usingTempToken && signedOut;

	const handleSubmit = async (mode: "vk" | "session" | "user") => {
		setSelectedMode(mode);
		try {
			const res = await submitFlow({
				flowId,
				body: { mode, value: mode === "vk" ? vkValue : undefined },
			}).unwrap();
			// Defence-in-depth: the server validates redirect_uri against the
			// registered client, but never hand a javascript:/data: URL to
			// location.href — it would execute in this origin. Block dangerous
			// schemes while still allowing http(s) and native custom-scheme
			// redirects that clients may register.
			if (!isSafeRedirect(res.redirect_url)) {
				toast.error(t("auth:consent.authFailed"), { description: t("auth:consent.invalidRedirectUrl") });
				setSelectedMode(null);
				return;
			}
			window.location.href = res.redirect_url;
		} catch (err) {
			toast.error(t("auth:consent.authFailed"), { description: getErrorMessage(err) });
			setSelectedMode(null);
		}
	};

	if (isLoading) return <FullPageLoader />;

	if (isError || !flow) {
		const status = (error as { status?: number } | undefined)?.status;
		if (status === 401) {
			// A rejected token is the only 401 that means the link is finished.
			// Anything else is fixed by having a session — go get one.
			if (getErrorCode(error) !== TEMP_TOKEN_REJECTED_CODE && signedOut) {
				return <RedirectToSignIn loginHref={loginHref} />;
			}
			return <InvalidLinkView />;
		}
		return (
			<Shell>
				<div className="text-center">
					<h1 className="text-xl font-semibold">{t("auth:consent.linkUnavailable")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">{t("auth:consent.linkUnavailableDesc")}</p>
				</div>
			</Shell>
		);
	}

	const hasUser = flow.available_modes.includes("user");
	const hasVK = flow.available_modes.includes("vk");
	const hasSession = flow.available_modes.includes("session");
	const hasAnyMode = hasUser || hasVK || hasSession;
	const clientName = flow.client_name || t("auth:consent.defaultClientName");

	return (
		<Shell>
			{/* Header */}
			<div className="mb-8 text-center">
				<div className="bg-primary/10 mx-auto mb-4 flex size-14 items-center justify-center rounded-full">
					<ShieldCheck className="text-primary size-7" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{t("auth:consent.wantsToConnect", { name: clientName })}</h1>
				<p className="text-muted-foreground mt-1.5 text-sm">{t("auth:consent.chooseIdentity")}</p>
				<p className="mt-4 text-sm">{t("auth:consent.recognizeWarning")}:</p>
				<p className="mt-2 font-mono text-sm break-all" data-testid="oauth-consent-redirect-uri">
					{flow.redirect_uri}
				</p>
			</div>

			<div className="space-y-3">
				{/* No mode available — nothing the user can act on here */}
				{!hasAnyMode && (
					<div className="rounded-sm border p-4 text-center" data-testid="oauth-consent-empty-state">
						<p className="text-sm font-medium">{t("auth:consent.noOptions")}</p>
						<p className="text-muted-foreground mt-1 text-xs">{t("auth:consent.restartConnection")}</p>
					</div>
				)}

				{/* User mode — most prominent when logged in */}
				{hasUser && flow.logged_in_user && (
					<div className="rounded-sm border p-4">
						<div className="flex items-center gap-3">
							<div className="bg-muted flex size-9 shrink-0 items-center justify-center rounded-full">
								<UserRound className="text-muted-foreground size-4" />
							</div>
							<div className="min-w-0 flex-1">
								<p className="text-sm leading-tight font-medium">{flow.logged_in_user.name || flow.logged_in_user.id}</p>
								<p className="text-muted-foreground text-xs">{t("auth:consent.signedInAccount")}</p>
							</div>
						</div>
						<Button
							data-testid="oauth-consent-continue-user-btn"
							className="mt-4 w-full"
							onClick={() => handleSubmit("user")}
							disabled={submitting}
						>
							{submitting && selectedMode === "user" ? (
								<>
									<Loader2 className="mr-2 size-4 animate-spin" />
									{t("auth:consent.connecting")}
								</>
							) : (
								t("auth:consent.continueAs", { name: flow.logged_in_user.name || flow.logged_in_user.id })
							)}
						</Button>
					</div>
				)}

				{/* User mode — sign in prompt when not logged in */}
				{hasUser && !flow.logged_in_user && showLoginOption && (
					<div className="rounded-sm border p-4">
						<div className="flex items-center gap-3">
							<div className="bg-muted flex size-9 shrink-0 items-center justify-center rounded-full">
								<UserRound className="text-muted-foreground size-4" />
							</div>
							<div>
								<p className="text-sm font-medium">{t("auth:consent.signInWithAccount")}</p>
								<p className="text-muted-foreground text-xs">{t("auth:consent.requiresAccount")}</p>
							</div>
						</div>
						<Button asChild variant="outline" className="mt-4 w-full">
							<a href={loginHref} data-testid="oauth-consent-signin-link">
								<LogIn className="mr-2 size-4" />
								{t("auth:consent.signInToContinue")}
							</a>
						</Button>
					</div>
				)}

				{/* Divider between user and key options */}
				{hasUser && (hasVK || hasSession) && (
					<div className="relative">
						<Separator />
						<span className="bg-card text-muted-foreground absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 px-2 text-xs">
							{t("auth:consent.or")}
						</span>
					</div>
				)}

				{/* VK mode */}
				{hasVK && (
					<div className="rounded-sm border p-4">
						<div className="mb-4 flex items-center gap-3">
							<div className="bg-muted flex size-9 shrink-0 items-center justify-center rounded-full">
								<KeyRound className="text-muted-foreground size-4" />
							</div>
							<div>
								<p className="text-sm font-medium">{t("auth:consent.virtualKey")}</p>
								<p className="text-muted-foreground text-xs">{t("auth:consent.virtualKeyDesc")}</p>
							</div>
						</div>
						<Input
							id="vk-input"
							data-testid="oauth-consent-vk-input"
							type="password"
							placeholder="sk-bf-…"
							value={vkValue}
							onChange={(e) => setVkValue(e.target.value)}
							onKeyDown={(e) => {
								if (e.key === "Enter" && vkValue.trim()) {
									void handleSubmit("vk");
								}
							}}
							disabled={submitting}
							className="mb-3"
						/>
						<Button
							data-testid="oauth-consent-connect-vk-btn"
							className="w-full"
							onClick={() => handleSubmit("vk")}
							disabled={submitting || !vkValue.trim()}
						>
							{submitting && selectedMode === "vk" ? (
								<>
									<Loader2 className="mr-2 size-4 animate-spin" />
									{t("auth:consent.connecting")}
								</>
							) : (
								t("auth:consent.connectWithKey")
							)}
						</Button>
						{hasUser && (
							<p className="text-muted-foreground mt-2.5 text-xs">{t("auth:consent.linkedKeyNote")}</p>
						)}
					</div>
				)}

				{hasVK && hasSession && (
					<div className="relative">
						<Separator />
						<span className="bg-card text-muted-foreground absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 px-2 text-xs">
							{t("auth:consent.or")}
						</span>
					</div>
				)}

				{/* Session mode — de-emphasised, last */}
				{hasSession && (
					<Button
						data-testid="oauth-consent-continue-session-btn"
						variant="ghost"
						className="text-muted-foreground hover:text-foreground h-auto w-full justify-start gap-3 px-4 py-3"
						onClick={() => handleSubmit("session")}
						disabled={submitting}
					>
						<Fingerprint className="size-4 shrink-0" />
						<div className="text-left">
							<span className="block text-sm font-normal">
								{submitting && selectedMode === "session" ? t("auth:consent.connecting") : t("auth:consent.continueWithoutIdentity")}
							</span>
							<span className="text-xs opacity-70">{t("auth:consent.anonymousSession")}</span>
						</div>
					</Button>
				)}
			</div>

			{/* Expiry */}
			<p className="text-muted-foreground mt-6 text-center text-xs">
				{t("auth:consent.expiresIn", { time: formatExpiry(flow.expires_at, t) })}
			</p>
		</Shell>
	);
}

function formatExpiry(iso: string, t: TFunction): string {
	const ts = new Date(iso).getTime();
	if (Number.isNaN(ts)) return t("auth:consent.expiry.soon");
	try {
		const diffMs = ts - Date.now();
		if (diffMs < 0) return t("auth:consent.expiry.soon");
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 1) return t("auth:consent.expiry.inLessThanAMinute");
		if (mins === 1) return t("auth:consent.expiry.inOneMinute");
		return t("auth:consent.expiry.inMinutes", { count: mins });
	} catch {
		return t("auth:consent.expiry.soon");
	}
}

function Shell({ children }: { children: React.ReactNode }) {
	const { t } = useTranslation(["common", "auth"]);
	return (
		<div className="mx-auto flex min-h-screen w-full items-center justify-center p-4 sm:p-6">
			<div className="bg-card w-full max-w-md rounded-sm border p-6 shadow-sm sm:p-8">{children}</div>
		</div>
	);
}

// Bounces a signed-out visitor through /login and back via ?goto= (SSO stashes
// it across the IdP hop). The card covers a blocked redirect.
function RedirectToSignIn({ loginHref }: { loginHref: string }) {
	const { t } = useTranslation(["common", "auth"]);
	useEffect(() => {
		// replace(), so Back from /login doesn't re-enter the redirect.
		window.location.replace(loginHref);
	}, [loginHref]);

	return (
		<Shell>
			<div className="text-center">
				<h1 className="text-xl font-semibold tracking-tight">{t("auth:consent.signInToContinue")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("auth:consent.redirectSignInDesc")}</p>
				<Button asChild className="mt-6 w-full">
					<a href={loginHref} data-testid="oauth-consent-signin-required-link">
						<LogIn className="mr-2 size-4" />
						{t("auth:consent.signInToBifrost")}
					</a>
				</Button>
			</div>
		</Shell>
	);
}

function InvalidLinkView() {
	const { t } = useTranslation(["common", "auth"]);
	return (
		<Shell>
			<div className="text-center">
				<h1 className="text-xl font-semibold tracking-tight">{t("auth:consent.invalidLinkTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("auth:consent.invalidLinkDesc")}</p>
			</div>
		</Shell>
	);
}