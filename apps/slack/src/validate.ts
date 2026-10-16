/**
 * Slack's own check of a Block Kit payload. `blocks.validate` is public, so it
 * needs no token, but it does need the network. It is the check vultus's
 * sandbox runs (`pnpm sandbox validate`).
 */
export async function validateBlocks(blocks: unknown[], view?: object) {
  const form = new URLSearchParams(
    view ? { view: JSON.stringify(view) } : { blocks: JSON.stringify(blocks) },
  );
  for (let attempt = 0; ; attempt++) {
    const res = await fetch("https://slack.com/api/blocks.validate", {
      method: "POST",
      body: form,
    });
    const body = (await res.json()) as {
      ok: boolean;
      error?: string;
      errors?: { message: string; pointer: string }[];
    };
    if (body.error !== "ratelimited" || attempt >= 5) return body;
    await new Promise((r) => setTimeout(r, Number(res.headers.get("retry-after") ?? "2") * 1000));
  }
}
