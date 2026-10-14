import { App } from "./app.tsx";
import { httpCollegium } from "./collegium.ts";
import { readConfig } from "./config.ts";
import { slackClient, socketMode } from "./slack.ts";

/** The permissions the app cannot work without. Others unlock more of the inbox, see the README. */
const NEEDED = ["runs:read", "runs:write", "agents:read"];

const log = (line: string) => console.log(`${new Date().toISOString()} ${line}`);

async function main() {
  const config = readConfig();
  const collegium = httpCollegium({ baseUrl: config.collegium.url, token: config.collegium.token });

  // The credential decides the workspace. The configured name only has to agree with it.
  const me = await collegium.me();
  if (me.tenant !== config.collegium.tenant) {
    throw new Error(
      "The collegium credential is for another workspace than COLLEGIUM_TENANT. Refusing to start.",
    );
  }
  const lacking = [...NEEDED, ...(config.collegium.client ? ["clients:read"] : [])].filter(
    (p) => !me.permissions.includes(p),
  );
  if (lacking.length > 0) {
    throw new Error(`The collegium credential lacks ${lacking.join(", ")}.`);
  }

  // The bot token decides the team in the same way.
  const slack = slackClient(config.slack.botToken);
  const who = await slack.call<{ team_id: string; user_id: string }>("auth.test");
  if (who.team_id !== config.slack.team) {
    throw new Error(
      "The Slack bot token is for another team than SLACK_TEAM_ID. Refusing to start.",
    );
  }

  const app = new App(slack, collegium, {
    team: config.slack.team,
    botUser: who.user_id,
    subject: me.subject,
    operators: config.slack.operators,
    clientId: config.collegium.client,
    defaultAgent: config.collegium.defaultAgent,
    publicUrl: config.collegium.publicUrl,
    log,
  });

  const stop = new AbortController();
  for (const signal of ["SIGINT", "SIGTERM"] as const) process.once(signal, () => stop.abort());
  stop.signal.addEventListener("abort", () => {
    log("stopping");
    // Turns in flight finish their Slack writes, then the process ends.
    const patience = new Promise((r) => setTimeout(r, 10_000));
    Promise.race([app.idle(), patience]).finally(() => process.exit(0));
  });

  // The inbox is followed beside the socket. If collegium goes away it is followed again.
  (async () => {
    while (!stop.signal.aborted) {
      await app.watchInbox(stop.signal).catch((e: Error) => log(`inbox watch ended: ${e.message}`));
      await new Promise((r) => setTimeout(r, 5_000));
    }
  })();

  log(`serving workspace ${me.tenant} as ${me.subject}`);
  await socketMode(slackClient(config.slack.appToken), (envelope) => app.handle(envelope), {
    log,
    signal: stop.signal,
  });
}

main().catch((e: Error) => {
  console.error(e.message);
  process.exit(1);
});
