/**
 * vultus is linked as TypeScript source and compiled as it is loaded. Files
 * outside this app's tsconfig are compiled with the classic JSX transform,
 * which expects `React` in scope, so it is put there once. Import this before
 * anything that renders.
 */
import * as React from "react";

(globalThis as { React?: unknown }).React ??= React;
