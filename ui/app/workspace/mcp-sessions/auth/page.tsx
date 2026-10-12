// Auth landing route for inline-401 URLs returned by the inference path.
//
// Both per-user-OAuth and per-user-headers flows land here. The URL shape
// is identical except for the kind discriminator:
//   - OAuth:   {base}/workspace/mcp-sessions/auth?flow={flowId}#t={token}
//   - Headers: {base}/workspace/mcp-sessions/auth?flow={flowId}&kind=headers#t={token}
//
// The temp token in the URL fragment binds the page request to the flow ID,
// letting anonymous browser visitors complete the flow without a dashboard
// session. The page picks the right backend (oauth/per-user/flows vs
// mcp/per-user-headers/flows) based on the kind param.
//
// - OAuth flow:    fetches pending flow metadata, on "Authenticate" click
//                  requests the upstream authorize URL and redirects the
//                  browser. Upstream redirects back to /api/oauth/callback
//                  which completes the flow server-side.
// - Headers flow:  fetches the same flow row plus the schema and renders
//                  the values form, submitting back to the flow row's
//                  PUT endpoint. The backend verifies upstream, upserts
//                  the credential, and consumes the flow row + temp token.

import HeadersForm from "@/components/headersForm";
import FullPageLoader from "@/components/fullPageLoader";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useToast } from "@/hooks/use-toast";
import { getActiveTempToken } from "@/lib/store/apis/tempToken";
import {
	getErrorCode,
	getErrorMessage,
	useGetMCPFlowDetailQuery,
	useGetMCPPerUserHeadersFlowQuery,
	useIsAuthEnabledQuery,
	useStartMCPFlowMutation,
	useSubmitMCPPerUserHeadersFlowMutation,
} from "@/lib/store";
import { MCPFlowDetail } from "@/lib/types/mcpSessions";
import { MCPHeadersFlowDetail } from "@/lib/types/mcpPerUserHeaders";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, ExternalLink, Fingerprint, KeyRound, Loader2, LogIn, ShieldCheck, TriangleAlert, UserRound } from "lucide-react";
import { useQueryState } from "nuqs";
import React from "react";
import { useEffect, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";

// Mirrors handlers.TempTokenRejectedCode: a presented token was refused.
const TEMP_TOKEN_REJECTED_CODE = "temp_token_rejected";

export default function MCPSessionsAuthPage() {
	const [flowId] = useQueryState("flow");
	const [kind] = useQueryState("kind");

	// Both surfaces ride on ?flow={id}; `kind=headers` selects the headers
	// backend. Default (no kind / kind=oauth) is the OAuth branch — keeps the
	// OAuth URL shape unchanged.
	if (kind === "headers" && flowId) {
		return <HeadersAuthView flowId={flowId} />;
	}
	return <OAuthAuthView />;
}

function OAuthAuthView() {
	const { t } = useTranslation(["common", "mcp"]);
	const { toast } = useToast();
	const [flowId] = useQueryState("flow");
	const skip = !flowId;
	const { data: flow, isLoading, isError, error } = useGetMCPFlowDetailQuery(flowId ?? "", { skip });
	const [startFlow, { isLoading: starting }] = useStartMCPFlowMutation();
	const { data: authState } = useIsAuthEnabledQuery();
	const [usingTempToken] = useState(() => getActiveTempToken() !== null);
	const loginGoto = useMemo(() => {
		if (typeof window !== "undefined") {
			return `${window.location.pathname}${window.location.search}`;
		}
		if (flowId) {
			return `/workspace/mcp-sessions/auth?flow=${encodeURIComponent(flowId)}`;
		}
		return "/workspace/mcp-sessions/auth";
	}, [flowId]);
	const loginHref = useMemo(() => `/login?goto=${encodeURIComponent(loginGoto)}`, [loginGoto]);
	const signedOut = authState?.is_auth_enabled === true && authState.has_valid_token === false;
	const showLoginOption = usingTempToken && signedOut;
	const showTempTokenSSOWarning = showLoginOption && authState.auth_type === "sso";

	if (!flowId) {
		return (
			<CenteredCard>
				<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.missingFlowTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">
					<Trans t={t} i18nKey="mcp:sessions.auth.missingFlowDesc" components={{ code: <code className="bg-muted rounded px-1 py-0.5" /> }}>
						This URL is missing the <code>flow</code> query parameter. Open the link from your inference response or the sessions tab.
					</Trans>
				</p>
				<div className="mt-6">
					<SessionsTabLink />
				</div>
			</CenteredCard>
		);
	}

	if (isLoading) {
		return <FullPageLoader />;
	}

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
		if (status === 403) {
			return (
				<CenteredCard>
					<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.notYoursTitle")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.notYoursBody")}</p>
					<div className="mt-6">
						<SessionsTabLink />
					</div>
				</CenteredCard>
			);
		}
		if (status === 404) {
			return (
				<CenteredCard>
					<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.expiredTitle")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.expiredBody")}</p>
					<div className="mt-6">
						<SessionsTabLink />
					</div>
				</CenteredCard>
			);
		}
		return (
			<CenteredCard>
				<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.loadFailedTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{getErrorMessage(error)}</p>
			</CenteredCard>
		);
	}

	// Flow row exists but isn't pending: it's already been completed, failed,
	// or expired. Don't show the "Authenticate" button since startFlow would
	// reject (BuildUpstreamAuthorizeURL rejects non-pending flows).
	if (flow.status !== "pending") {
		return <CompletedFlowView flow={flow} />;
	}

	const handleAuthenticate = async () => {
		try {
			const res = await startFlow(flowId).unwrap();
			window.location.href = res.authorize_url;
		} catch (err) {
			toast({
				title: t("mcp:sessions.auth.startFailed"),
				description: getErrorMessage(err),
				variant: "destructive",
			});
		}
	};

	const mcpClientName = flow.mcp_client?.name || flow.mcp_client?.client_id || t("mcp:sessions.auth.fallbackServerName");
	const isReauth = flow.has_active_token === true;

	return (
		<CenteredCard>
			<div className="bg-primary/10 mb-5 flex size-12 items-center justify-center rounded-full">
				<ShieldCheck className="text-primary size-6" />
			</div>
			<h1 className="text-xl font-semibold tracking-tight">
				{isReauth ? t("mcp:sessions.auth.titleReauth", { name: mcpClientName }) : t("mcp:sessions.auth.titleAuth", { name: mcpClientName })}
			</h1>
			<p className="text-muted-foreground mt-2 text-sm">
				{isReauth ? (
					<Trans t={t} i18nKey="mcp:sessions.auth.reauthDesc" components={{ strong: <strong /> }}>
						An active credential already exists for the binding below. Completing this flow will <strong>replace</strong> it with a fresh
						credential. You can also close this tab to keep using the existing one.
					</Trans>
				) : (
					t("mcp:sessions.auth.authDesc")
				)}
			</p>

			<dl className="bg-muted/40 mt-6 space-y-3 rounded-sm border p-4 text-sm">
				<DetailRow label={t("mcp:sessions.auth.detailMcpClient")} value={mcpClientName} mono={!flow.mcp_client?.name} />
				<DetailRow label={t("mcp:sessions.auth.detailBoundTo")} value={<BindingValue flow={flow} />} />
				<DetailRow label={t("mcp:sessions.auth.detailFlowExpires")} value={formatExpiry(flow.expires_at, t)} />
			</dl>

			<div className="mt-6 flex gap-3">
				<Button onClick={handleAuthenticate} disabled={starting} data-testid="mcp-auth-authenticate-button">
					{starting ? <Loader2 className="size-4 animate-spin" /> : <ExternalLink className="size-4" />}
					<span>{isReauth ? t("mcp:sessions.reauthenticate") : t("mcp:sessions.auth.authenticate")}</span>
				</Button>
				{showLoginOption && !showTempTokenSSOWarning ? (
					<Button asChild variant="outline" data-testid="mcp-auth-login-instead-inline-button">
						<a href={loginHref}>
							<LogIn className="size-4" />
							{t("mcp:sessions.auth.loginInstead")}
						</a>
					</Button>
				) : null}
				<SessionsTabLink variant="ghost" />
			</div>
		</CenteredCard>
	);
}

// HeadersAuthView renders the per-user-headers submission form. Fetches the
// pending flow row + schema from /api/mcp/per-user-headers/flows/{id},
// renders one input per required key, PUTs the values back to the same flow
// endpoint. On success the backend verifies upstream, upserts the credential
// row, deletes the flow row + temp token, and we show a success card.
function HeadersAuthView({ flowId }: { flowId: string }) {
	const { t } = useTranslation(["common", "mcp"]);
	const { toast } = useToast();
	const { data: authState } = useIsAuthEnabledQuery();
	const [submitted, setSubmitted] = useQueryState("submitted");
	// Skip the GET once the submit has completed — the backend deletes the
	// flow row on success, so any refetch returns 404 and we'd render the
	// "expired" view on top of a freshly successful submission.
	const { data: detail, isLoading, isError, error } = useGetMCPPerUserHeadersFlowQuery(flowId, { skip: submitted === "true" });
	const [submit, { isLoading: submitting }] = useSubmitMCPPerUserHeadersFlowMutation();

	if (submitted === "true") {
		return (
			<CenteredCard>
				<div className="mb-5 flex size-12 items-center justify-center rounded-full bg-emerald-500/10">
					<CheckCircle2 className="size-6 text-emerald-600" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{t("mcp:sessions.auth.headersSavedTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.headersSavedBody")}</p>
				<div className="mt-6 flex gap-3">
					<SessionsTabLink />
				</div>
			</CenteredCard>
		);
	}

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (isError || !detail) {
		const status = (error as { status?: number } | undefined)?.status;
		if (status === 401) {
			// Same reasoning as the OAuth branch above.
			const signedOut = authState?.is_auth_enabled === true && authState.has_valid_token === false;
			if (getErrorCode(error) !== TEMP_TOKEN_REJECTED_CODE && signedOut) {
				const returnPath = `/workspace/mcp-sessions/auth?flow=${encodeURIComponent(flowId)}&kind=headers`;
				return <RedirectToSignIn loginHref={`/login?goto=${encodeURIComponent(returnPath)}`} />;
			}
			return <InvalidLinkView />;
		}
		// Enterprise RBAC middleware short-circuits flow endpoints with 403
		// when the caller doesn't own the flow. Mirrors the OAuth branch above
		// so headers flows get the same dedicated card instead of falling
		// through to the generic load-failure state.
		if (status === 403) {
			return (
				<CenteredCard>
					<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.headersNotYoursTitle")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.notYoursBody")}</p>
					<div className="mt-6">
						<SessionsTabLink />
					</div>
				</CenteredCard>
			);
		}
		if (status === 404 || status === 410) {
			return (
				<CenteredCard>
					<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.headersExpiredTitle")}</h1>
					<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.headersExpiredBody")}</p>
					<div className="mt-6">
						<SessionsTabLink />
					</div>
				</CenteredCard>
			);
		}
		return (
			<CenteredCard>
				<h1 className="text-xl font-semibold">{t("mcp:sessions.auth.headersLoadFailedTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{getErrorMessage(error)}</p>
			</CenteredCard>
		);
	}

	const handleSubmit = async (values: Record<string, string>) => {
		try {
			await submit({ flowId, body: { headers: values } }).unwrap();
			void setSubmitted("true");
		} catch (err) {
			toast({
				title: t("mcp:sessions.auth.submitFailed"),
				description: getErrorMessage(err),
				variant: "destructive",
			});
		}
	};

	const mcpClientName = detail.mcp_client?.name || detail.mcp_client?.client_id || t("mcp:sessions.auth.fallbackServerName");
	const isEdit = detail.has_active_credential;
	const title = isEdit
		? t("mcp:sessions.auth.headersTitleEdit", { name: mcpClientName })
		: t("mcp:sessions.auth.headersTitleNew", { name: mcpClientName });

	return (
		<CenteredCard>
			<div className="bg-primary/10 mb-5 flex size-12 items-center justify-center rounded-full">
				<ShieldCheck className="text-primary size-6" />
			</div>
			<h1 className="text-xl font-semibold tracking-tight">{title}</h1>
			<p className="text-muted-foreground mt-2 text-sm">
				{isEdit ? (
					<Trans t={t} i18nKey="mcp:sessions.auth.headersEditDesc" components={{ strong: <strong /> }}>
						This server already has stored credentials for you. Submitting new values <strong>replaces</strong> the existing entry; the
						server will be re-verified before saving.
					</Trans>
				) : (
					t("mcp:sessions.auth.headersNewDesc")
				)}
			</p>

			<dl className="bg-muted/40 mt-6 space-y-3 rounded-sm border p-4 text-sm">
				<DetailRow label={t("mcp:sessions.auth.detailMcpClient")} value={mcpClientName} mono={!detail.mcp_client?.name} />
				<DetailRow label={t("mcp:sessions.auth.detailBoundTo")} value={<HeadersBindingValue flow={detail} />} />
				<DetailRow label={t("mcp:sessions.auth.detailFlowExpires")} value={formatExpiry(detail.expires_at, t)} />
			</dl>

			<div className="mt-6">
				<HeadersForm
					requiredKeys={detail.required_header_keys}
					adminHeaderKeys={detail.admin_header_keys}
					previouslySubmittedKeys={detail.submitted_keys}
					onSubmit={handleSubmit}
					busy={submitting}
					submitLabel={isEdit ? t("common:actions.save") : t("common:actions.submit")}
					testIdPrefix="mcp-headers-submit"
				/>
			</div>
		</CenteredCard>
	);
}

function HeadersBindingValue({ flow }: { flow: MCPHeadersFlowDetail }) {
	const { t } = useTranslation(["common", "mcp"]);
	if (flow.flow_mode === "user") {
		const userID = flow.user_id;
		if (!userID) {
			return (
				<span className="inline-flex items-center gap-2">
					<UserRound className="text-muted-foreground size-3.5" />
					<Badge variant="secondary">{t("mcp:sessions.auth.badgeFirstUser")}</Badge>
				</span>
			);
		}
		const displayName = flow.user?.name || flow.user?.email;
		return (
			<span className="inline-flex items-center gap-2">
				<UserRound className="text-muted-foreground size-3.5" />
				{displayName ? <span>{displayName}</span> : <span className="font-mono">{userID}</span>}
			</span>
		);
	}
	if (flow.flow_mode === "vk" && flow.virtual_key) {
		return (
			<span className="inline-flex items-center gap-2">
				<KeyRound className="text-muted-foreground size-3.5" />
				<span>{flow.virtual_key.name || flow.virtual_key.id}</span>
			</span>
		);
	}
	if (flow.flow_mode === "session" && flow.session_id) {
		return (
			<span className="inline-flex items-center gap-2">
				<Fingerprint className="text-muted-foreground size-3.5" />
				<span className="font-mono">{flow.session_id}</span>
			</span>
		);
	}
	return <span className="text-muted-foreground italic">{t("common:state.unknown")}</span>;
}

function CompletedFlowView({ flow }: { flow: MCPFlowDetail }) {
	const { t } = useTranslation(["common", "mcp"]);
	const mcpClientName = flow.mcp_client?.name || flow.mcp_client?.client_id || t("mcp:sessions.auth.fallbackThisServer");
	// has_active_token wins over the flow's row status: a pending flow with an
	// existing active token means OAuth was re-initiated unnecessarily.
	const effectivelyAuthorized = flow.status === "authorized" || flow.has_active_token;
	const title = effectivelyAuthorized
		? t("mcp:sessions.auth.completedAlready")
		: flow.status === "expired"
			? t("mcp:sessions.auth.completedExpired")
			: t("mcp:sessions.auth.completedDone");
	const body = effectivelyAuthorized
		? t("mcp:sessions.auth.completedAlreadyBody", { name: mcpClientName })
		: t("mcp:sessions.auth.completedRetryBody");
	return (
		<CenteredCard>
			<h1 className="text-xl font-semibold tracking-tight">{title}</h1>
			<p className="text-muted-foreground mt-2 text-sm">{body}</p>
			<dl className="bg-muted/40 mt-6 space-y-3 rounded-sm border p-4 text-sm">
				<DetailRow label={t("mcp:sessions.auth.detailMcpClient")} value={mcpClientName} mono={!flow.mcp_client?.name} />
				<DetailRow label={t("mcp:sessions.auth.detailBoundTo")} value={<BindingValue flow={flow} />} />
			</dl>
			<div className="mt-6">
				<SessionsTabLink />
			</div>
		</CenteredCard>
	);
}

function DetailRow({ label, value, mono = false }: { label: string; value: React.ReactNode; mono?: boolean }) {
	return (
		<div className="flex items-start justify-between gap-4">
			<dt className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</dt>
			<dd className={`text-right text-sm ${mono ? "font-mono" : ""}`}>{value}</dd>
		</div>
	);
}

function BindingValue({ flow }: { flow: MCPFlowDetail }) {
	const { t } = useTranslation(["common", "mcp"]);
	if (flow.flow_mode === "user") {
		const userID = flow.user_id;
		if (!userID) {
			return (
				<span className="inline-flex items-center gap-2">
					<UserRound className="text-muted-foreground size-3.5" />
					<Badge variant="secondary">{t("mcp:sessions.auth.badgeFirstUser")}</Badge>
				</span>
			);
		}
		const displayName = flow.user?.name || flow.user?.email;
		return (
			<span className="inline-flex items-center gap-2">
				<UserRound className="text-muted-foreground size-3.5" />
				{displayName ? <span>{displayName}</span> : <span className="font-mono">{userID}</span>}
			</span>
		);
	}
	if (flow.flow_mode === "vk" && flow.virtual_key) {
		return (
			<span className="inline-flex items-center gap-2">
				<KeyRound className="text-muted-foreground size-3.5" />
				<span>{flow.virtual_key.name || flow.virtual_key.id}</span>
			</span>
		);
	}
	if (flow.flow_mode === "session" && flow.session_id) {
		return (
			<span className="inline-flex items-center gap-2">
				<Fingerprint className="text-muted-foreground size-3.5" />
				<span className="font-mono">{flow.session_id}</span>
			</span>
		);
	}
	return <span className="text-muted-foreground italic">{t("common:state.unknown")}</span>;
}

type ExpiryT = (key: string, opts?: Record<string, unknown>) => string;

function formatExpiry(iso: string, t: ExpiryT): string {
	try {
		const time = new Date(iso).getTime();
		if (Number.isNaN(time)) return iso;
		const diffMs = time - Date.now();
		if (diffMs < 0) return t("mcp:sessions.auth.expiryExpired");
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 1) return t("mcp:sessions.auth.expiryLess");
		if (mins === 1) return t("mcp:sessions.auth.expiryOne");
		return t("mcp:sessions.auth.expiryMinutes", { count: mins });
	} catch {
		return iso;
	}
}

function CenteredCard({ children }: { children: React.ReactNode }) {
	return (
		<div className="mx-auto flex min-h-[calc(100dvh-6rem)] w-full max-w-xl items-center justify-center p-6">
			<div className="bg-card w-full rounded-sm border p-8 shadow-sm">{children}</div>
		</div>
	);
}

function SessionsTabLink({ variant = "outline" }: { variant?: "outline" | "ghost" }) {
	const { t } = useTranslation(["common", "mcp"]);
	// Hide the link only when the visitor has no dashboard session — for them,
	// /workspace/mcp-sessions would 401 and bounce to /login. Admins (cookie
	// present) still see it. ClientLayout already cached this query for the
	// route, so this is a free hook call.
	const { data: authState } = useIsAuthEnabledQuery();
	if (authState?.is_auth_enabled && !authState.has_valid_token) {
		return null;
	}
	return (
		<Button asChild variant={variant} data-testid="mcp-auth-sessions-tab-link">
			<Link to="/workspace/mcp-sessions">{t("mcp:sessions.auth.openSessionsTab")}</Link>
		</Button>
	);
}

// Bounces a signed-out visitor through /login and back via ?goto= (SSO stashes
// it across the IdP hop). The card covers a blocked redirect.
function RedirectToSignIn({ loginHref }: { loginHref: string }) {
	const { t } = useTranslation(["common", "mcp"]);
	useEffect(() => {
		// replace(), so Back from /login doesn't re-enter the redirect.
		window.location.replace(loginHref);
	}, [loginHref]);

	return (
		<CenteredCard>
			<div className="bg-primary/10 mb-5 flex size-12 items-center justify-center rounded-full">
				<LogIn className="text-primary size-6" />
			</div>
			<h1 className="text-xl font-semibold tracking-tight">{t("mcp:sessions.auth.signInTitle")}</h1>
			<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.signInBody")}</p>
			<div className="mt-6">
				<Button asChild data-testid="mcp-auth-signin-required-link">
					<a href={loginHref}>
						<LogIn className="size-4" />
						{t("mcp:sessions.auth.signInButton")}
					</a>
				</Button>
			</div>
		</CenteredCard>
	);
}

// A 401 the visitor's own session can't explain: the mcp_auth temp token itself
// was refused. Signed-out visitors are sent to sign in instead.
function InvalidLinkView() {
	const { t } = useTranslation(["common", "mcp"]);
	return (
		<CenteredCard>
			<h1 className="text-xl font-semibold tracking-tight">{t("mcp:sessions.auth.invalidLinkTitle")}</h1>
			<p className="text-muted-foreground mt-2 text-sm">{t("mcp:sessions.auth.invalidLinkBody")}</p>
		</CenteredCard>
	);
}