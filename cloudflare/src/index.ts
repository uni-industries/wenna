// The Worker in front of wenna on Cloudflare Containers (HOSTING.md). Every
// request goes to one named container, so exactly one instance holds the
// database; the image (../Dockerfile) keeps it in the user's R2 bucket.
import { Container, getContainer } from "@cloudflare/containers";
import { isSet, optionalVars, sleepAfter } from "./settings";

interface Env {
	WENNA: DurableObjectNamespace<Wenna>;
	WENNA_TOKEN: string;
	R2_ACCOUNT_ID: string;
	R2_BUCKET: string;
	R2_ACCESS_KEY_ID: string;
	R2_SECRET_ACCESS_KEY: string;
	// Idle time before the container sleeps, e.g. "30m"; the library's default
	// when unset.
	WENNA_SLEEP_AFTER?: string;
	WENNA_PUBLIC_URL?: string;
	WENNA_ACCESS_TEAM_DOMAIN?: string;
	WENNA_ACCESS_AUD?: string;
	WENNA_ACCESS_OWNER_EMAIL?: string;
	WENNA_WORKOS_ISSUER?: string;
	WENNA_WORKOS_RESOURCE?: string;
	WENNA_WORKOS_OWNER_ID?: string;
}

const secrets = [
	"WENNA_TOKEN",
	"R2_ACCOUNT_ID",
	"R2_BUCKET",
	"R2_ACCESS_KEY_ID",
	"R2_SECRET_ACCESS_KEY",
] as const;

// wenna's optional settings, passed through unchanged when set (settings.ts).
const optional = [
	"WENNA_PUBLIC_URL",
	"WENNA_ACCESS_TEAM_DOMAIN",
	"WENNA_ACCESS_AUD",
	"WENNA_ACCESS_OWNER_EMAIL",
	"WENNA_WORKOS_ISSUER",
	"WENNA_WORKOS_RESOURCE",
	"WENNA_WORKOS_OWNER_ID",
] as const;

export class Wenna extends Container<Env> {
	defaultPort = 8080;

	constructor(ctx: DurableObjectState<{}>, env: Env) {
		super(ctx, env);
		// An invalid value keeps the library's default here; fetch refuses it.
		const sleep = sleepAfter(env.WENNA_SLEEP_AFTER);
		if (sleep) {
			this.sleepAfter = sleep;
		}
		this.envVars = {
			WENNA_TOKEN: env.WENNA_TOKEN,
			// Cloudflare terminates HTTPS before the request reaches the Worker.
			WENNA_BEHIND_TLS_PROXY: "true",
			REPLICA_ENDPOINT: `https://${env.R2_ACCOUNT_ID}.r2.cloudflarestorage.com`,
			REPLICA_BUCKET: env.R2_BUCKET,
			LITESTREAM_ACCESS_KEY_ID: env.R2_ACCESS_KEY_ID,
			LITESTREAM_SECRET_ACCESS_KEY: env.R2_SECRET_ACCESS_KEY,
			...optionalVars(optional, env),
		};
	}
}

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		// An http:// request has already sent its token in the clear; refuse it
		// so the app's owner notices and switches to https://.
		if (new URL(request.url).protocol !== "https:") {
			return new Response("Use the https:// address.\n", { status: 403 });
		}
		// Never start wenna without its bucket: its memory would vanish when
		// the container sleeps.
		const missing = secrets.filter((name) => !isSet(env[name]));
		if (missing.length > 0) {
			console.error("wenna is missing secrets", missing);
			return new Response("This memory is not set up yet.\n", { status: 503 });
		}
		// The container library throws on a sleep time it cannot read; refuse
		// before starting anything.
		if (sleepAfter(env.WENNA_SLEEP_AFTER) === null) {
			console.error(
				'WENNA_SLEEP_AFTER must be a whole number then s, m or h, e.g. "30m"',
			);
			return new Response("This memory is not set up yet.\n", { status: 503 });
		}
		return getContainer(env.WENNA).fetch(request);
	},
};
