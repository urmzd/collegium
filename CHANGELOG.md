# Changelog

## 0.4.0 (2026-10-16)

### Features

- support seamless Okta and Tailscale console entry (#24) ([0908386](https://github.com/urmzd/collegium/commit/0908386f9bc333488b58a553e25d0ef288028cd2))

[Full Changelog](https://github.com/urmzd/collegium/compare/v0.3.0...v0.4.0)


## 0.3.0 (2026-10-16)

### Features

- add prompt-first Home and clear project navigation (#23) ([e12115e](https://github.com/urmzd/collegium/commit/e12115e25d215c7618f99f290143dd47be846093))

### Misc

- **deps**: upgrade saige to v0.37.0 (#22) ([907768c](https://github.com/urmzd/collegium/commit/907768c796ad86bea27083254a8ed4d4d15abb3f))

[Full Changelog](https://github.com/urmzd/collegium/compare/v0.2.1...v0.3.0)


## 0.2.1 (2026-10-16)

### Bug Fixes

- fit agent orbs naturally in compact console rows (#21) ([7c2adbb](https://github.com/urmzd/collegium/commit/7c2adbbb5f1a629dfc081256f797d3f07a6924dd))

[Full Changelog](https://github.com/urmzd/collegium/compare/v0.2.0...v0.2.1)


## 0.2.0 (2026-10-16)

### Features

- organize tasks and add structured helper calls (#19) ([748f0d0](https://github.com/urmzd/collegium/commit/748f0d001c5d98377a6dc345eb09804931b0e088))

### Bug Fixes

- preserve answer attribution across resumed events (#20) ([9648bc1](https://github.com/urmzd/collegium/commit/9648bc16e1e4e57452e3a18544fe992021910a1e))

[Full Changelog](https://github.com/urmzd/collegium/compare/v0.1.0...v0.2.0)


## 0.1.0 (2026-10-15)

### Breaking

- stop a run through the runtime, with tenant-scoped run ids (#10) ([8e73e34](https://github.com/urmzd/collegium/commit/8e73e341da96b55573f19dbc6bf022f45892f8f0))
- rename open-agent-platform to collegium (#3) ([c0a7a4a](https://github.com/urmzd/collegium/commit/c0a7a4a9500833765b62b9b884a217e5427d3f66))

### Features

- park runs on people and answer their questions in the inbox, on the run and in Slack (#17) ([fcfcc5c](https://github.com/urmzd/collegium/commit/fcfcc5c5423f77ba72340c30ba6edd280a433514))
- record usage per run and enforce spend limits (#16) ([1712eea](https://github.com/urmzd/collegium/commit/1712eeac75fa4f92551a3e5a7af18f8ca62692de))
- **web**: Home notifications from alerts and the inbox, each linking to its own page (#14) ([7bf8cf7](https://github.com/urmzd/collegium/commit/7bf8cf764e7f3737491930c9e26b461429c7488f))
- **slack**: Socket Mode Slack app on a shared collegium-to-vultus adapter (#9) ([63592be](https://github.com/urmzd/collegium/commit/63592bef9e807c46a6116ffbd3ee3a1210da8728))
- **web**: a console home on vultus Home, a grouped menu, and an access explanation (#13) ([42d7d83](https://github.com/urmzd/collegium/commit/42d7d8301eb0ad46376474b5792c8b1b82a76af6))
- **auth**: an authorization policy language and a mock OIDC provider for development (#11) ([2dc7107](https://github.com/urmzd/collegium/commit/2dc7107f69988bf9dec550702110a18140ef5426))
- OpenAPI contract, drift tests and a generated TypeScript client (#8) ([26da25e](https://github.com/urmzd/collegium/commit/26da25eb77679a5161f11f7334be1a3e184dffb1))
- GitHub and Slack integrations with clients per installation (#7) ([dd795b4](https://github.com/urmzd/collegium/commit/dd795b474dfb93dba5db1f545233700b969bc87b))
- **web**: a limits panel in the session gauge's gear, on fixtures ([206a258](https://github.com/urmzd/collegium/commit/206a25827d3bddc218c9487e94464b3752efa1cb))
- called agents as runs of their own, attached or delegated, with stop ([73d1289](https://github.com/urmzd/collegium/commit/73d1289bd10757f4eb0b62f5443afd74a88240f9))
- inbox, artifacts and storage, activity drawer, copy as, session gauge, limits pages, and motion ([2fbba13](https://github.com/urmzd/collegium/commit/2fbba13b66b5bc0bdeaf9d603afa2c999a05906d))
- onboarding, projects, usage limits, paged recents, agent strokes and console screenshots ([41474de](https://github.com/urmzd/collegium/commit/41474de7d29baf622ffdce115390b4b86a36e29c))
- memory approval, the orb as each agent's mark, menus, and a flatter console ([c624178](https://github.com/urmzd/collegium/commit/c624178fdb93f91018b5b96bdbd3c6d38c61f901))
- a composer with attach and dictation, agent memory, conversation titles, and knowledge variants ([d218f44](https://github.com/urmzd/collegium/commit/d218f4430aa429067d89722d2f9da5c12abaad1d))
- **web**: drop the top bar, color-code agents and recents, and flatten the composer dock ([af62e2f](https://github.com/urmzd/collegium/commit/af62e2f0f8e2e889e9c53bf1449eaac59ee53683))
- conversations with branches, agent editor, evals, preferences and a glass console ([cab1d91](https://github.com/urmzd/collegium/commit/cab1d914ac515b18bd4a9ffc7f036c90df301d1e))
- server, sign-in, console, schedules, analytics, alerts and knowledge ([00c5cf4](https://github.com/urmzd/collegium/commit/00c5cf425a1894a22762428cf0fcab89e61ec758))

### Bug Fixes

- **slack**: never repeat a broadcast mention or link as the app (#15) ([62a9e82](https://github.com/urmzd/collegium/commit/62a9e82a2f76591bd744e27778429a0188150e69))
- **client**: close streams, resume dropped ones, verify oag, tighten the contract tests (#12) ([351a4aa](https://github.com/urmzd/collegium/commit/351a4aa7e63ec148030421bd04790b6d5322e210))

### Misc

- release from main after the full CI suite (#18) ([dca9cb4](https://github.com/urmzd/collegium/commit/dca9cb4f4bfae7b6df1baca05d136963e1d13214))
- pin opentag v0.2.0 and saige v0.34.0, and drop the local replace ([b946480](https://github.com/urmzd/collegium/commit/b9464807cfe2f551bea6cbe6110a2fc99cef7261))
- initial project scaffold with the call primitive ([cc84e42](https://github.com/urmzd/collegium/commit/cc84e42251dd341500680aa505314b1a44aaa020))
