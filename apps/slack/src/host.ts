/**
 * What a Slack host needs that vultus does not export yet. The entry ids and
 * the wording below are copied from vultus's Slack sandbox
 * (`apps/slack-sandbox/src/manifest.ts` and `serve.tsx`), where they are
 * private to that app. They belong in a vultus package: until they move, this
 * file is the copy, and it holds nothing else.
 */

/** Ways into the agent besides a direct message or a mention. They match the sandbox manifest. */
export const ENTRY = {
  /** Global shortcut: ask from anywhere, answered in the app's messages with that person. */
  ask: "aui_ask",
  /** Message shortcut: ask about one message, answered in its thread. */
  askAbout: "aui_ask_about",
  command: "/agent",
  /** The one text form behind the global shortcut. */
  form: "aui_form",
} as const;

export const words = {
  askTitle: "Ask the agent",
  askLabel: "What do you need?",
  send: "Send",
  cancel: "Cancel",
  notInChannel: "I'm not in this channel yet. Invite me with `/invite`, then try again.",
};

/** The form the global shortcut opens. Its text comes back on submit. */
export function askForm() {
  return {
    type: "modal",
    callback_id: ENTRY.form,
    private_metadata: JSON.stringify({ kind: "ask" }),
    title: { type: "plain_text", text: words.askTitle },
    submit: { type: "plain_text", text: words.send },
    close: { type: "plain_text", text: words.cancel },
    blocks: [
      {
        type: "input",
        block_id: "text",
        label: { type: "plain_text", text: words.askLabel },
        element: { type: "plain_text_input", action_id: "value", multiline: true },
      },
    ],
  };
}
