import assert from "node:assert/strict";
import { test } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { HomePage } from "./HomePage.tsx";

const prompt = <form aria-label="Message"><textarea placeholder="Ask anything" /><button type="submit">Send message</button></form>;

test("Home puts the prompt on the page before a conversation starts", () => {
  const html = renderToStaticMarkup(<HomePage greeting="Good morning, Ada" canRun hasAgents>{prompt}</HomePage>);
  assert.match(html, /Good morning, Ada/);
  assert.match(html, /<textarea/);
  assert.match(html, /Send message/);
  assert.doesNotMatch(html, /Recent conversations|Your projects|Open inbox|Reference|Start a conversation/);
});

test("Home hides the prompt when the person cannot run agents", () => {
  const html = renderToStaticMarkup(<HomePage greeting="Hello" canRun={false} hasAgents>{prompt}</HomePage>);
  assert.doesNotMatch(html, /<textarea|<form/);
  assert.match(html, /do not have permission/);
});

test("Home explains why a workspace with no agents cannot start a conversation", () => {
  const html = renderToStaticMarkup(<HomePage greeting="Hello" canRun hasAgents={false}>{prompt}</HomePage>);
  assert.doesNotMatch(html, /<textarea|<form/);
  assert.match(html, /agent needs to be configured/);
});
