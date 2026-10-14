import { clientCredentials, staticToken, type TokenSource } from "./collegium.ts";

export interface Config {
  slack: {
    botToken: string;
    appToken: string;
    /** The one Slack team this instance serves. Events from any other are refused. */
    team: string;
    /** Slack user ids who may settle any inbox item from Slack, and see the whole inbox on Home. */
    operators: string[];
  };
  collegium: {
    url: string;
    /** The one workspace this instance serves. Startup fails if the credential is for another. */
    tenant: string;
    token: TokenSource;
    /** The client record this app is, when it is one. */
    client?: string;
    defaultAgent?: string;
    /** Where people reach the console, for links to files. */
    publicUrl?: string;
  };
}

const list = (v: string | undefined) =>
  (v ?? "")
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);

/** Read the environment. Every missing name is reported at once, and no value is ever echoed. */
export function readConfig(env: Record<string, string | undefined> = process.env): Config {
  const missing: string[] = [];
  const need = (name: string) => {
    const v = env[name]?.trim();
    if (!v) missing.push(name);
    return v ?? "";
  };
  const slack = {
    botToken: need("SLACK_BOT_TOKEN"),
    appToken: need("SLACK_APP_TOKEN"),
    team: need("SLACK_TEAM_ID"),
    operators: list(env.SLACK_OPERATORS),
  };
  const tenant = need("COLLEGIUM_TENANT");
  const tokenUrl = env.COLLEGIUM_OIDC_TOKEN_URL?.trim();
  let token: TokenSource = staticToken(env.COLLEGIUM_TOKEN?.trim());
  if (tokenUrl) {
    token = clientCredentials({
      tokenUrl,
      clientId: need("COLLEGIUM_OIDC_CLIENT_ID"),
      clientSecret: need("COLLEGIUM_OIDC_CLIENT_SECRET"),
      scopes: list(env.COLLEGIUM_OIDC_SCOPES),
      audience: env.COLLEGIUM_OIDC_AUDIENCE?.trim() || undefined,
    });
  }
  if (missing.length > 0) {
    throw new Error(
      `Missing ${missing.join(", ")}. Copy apps/slack/.envrc.local.example to the repository root as .envrc.local, fill it in and run direnv allow.`,
    );
  }
  return {
    slack,
    collegium: {
      url: env.COLLEGIUM_URL?.trim() || "http://127.0.0.1:8080",
      tenant,
      token,
      client: env.COLLEGIUM_CLIENT?.trim() || undefined,
      defaultAgent: env.COLLEGIUM_DEFAULT_AGENT?.trim() || undefined,
      publicUrl: env.COLLEGIUM_PUBLIC_URL?.trim().replace(/\/+$/, "") || undefined,
    },
  };
}
