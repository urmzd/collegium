/**
 * The one adapter between collegium and vultus. It has no React, no DOM and no
 * Slack in it: the platform's wire shapes go in, vultus's model comes out, and
 * vultus's actions go back as platform calls.
 */
export * from "./fold.ts";
export * from "./mail.ts";
export * from "./run.ts";
