import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const sourcePath = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../src/services/clientApi.js",
);
const source = readFileSync(sourcePath, "utf8");
const needle = "withApiLogging(";
const calls = [];
let searchFrom = 0;

while (searchFrom < source.length) {
  const start = source.indexOf(needle, searchFrom);
  if (start === -1) {
    break;
  }
  const before = source.slice(Math.max(0, start - 32), start);
  if (/\bfunction\s+$/.test(before)) {
    searchFrom = start + needle.length;
    continue;
  }
  const openParenIdx = start + needle.length - 1;
  const { args, end } = parseCallArgs(source, openParenIdx);
  calls.push({ args, snippet: source.slice(start, Math.min(end, start + 80)) });
  searchFrom = end;
}

if (calls.length === 0) {
  throw new Error("expected withApiLogging call sites in clientApi.js");
}

const badCalls = calls.filter((call) => {
  if (call.args.length !== 2) {
    return true;
  }
  return !isFunctionArg(call.args[1]);
});

if (badCalls.length > 0) {
  const details = badCalls
    .map((call) => `${call.args.length} args: ${call.snippet.replace(/\s+/g, " ")}`)
    .join("\n");
  throw new Error(`withApiLogging must be called as (name, runner):\n${details}`);
}

console.log(`clientApi logging contract tests passed (${calls.length} call sites)`);

function parseCallArgs(text, openParenIdx) {
  let depth = 0;
  let braces = 0;
  let brackets = 0;
  let inString = null;
  let escaped = false;
  let current = "";
  const args = [];

  for (let i = openParenIdx; i < text.length; i += 1) {
    const ch = text[i];
    if (inString) {
      current += ch;
      if (escaped) {
        escaped = false;
        continue;
      }
      if (ch === "\\") {
        escaped = true;
        continue;
      }
      if (ch === inString) {
        inString = null;
      }
      continue;
    }
    if (ch === "'" || ch === '"' || ch === "`") {
      inString = ch;
      if (depth > 0) {
        current += ch;
      }
      continue;
    }
    if (ch === "(") {
      depth += 1;
      if (depth > 1) {
        current += ch;
      }
      continue;
    }
    if (ch === ")") {
      depth -= 1;
      if (depth === 0) {
        if (current.trim()) {
          args.push(current.trim());
        }
        return { args, end: i + 1 };
      }
      current += ch;
      continue;
    }
    if (depth === 1 && braces === 0 && brackets === 0 && ch === ",") {
      args.push(current.trim());
      current = "";
      continue;
    }
    if (ch === "{") {
      braces += 1;
    } else if (ch === "}") {
      braces -= 1;
    } else if (ch === "[") {
      brackets += 1;
    } else if (ch === "]") {
      brackets -= 1;
    }
    current += ch;
  }
  throw new Error("unclosed withApiLogging call");
}

function isFunctionArg(arg) {
  return /^(?:async\s+)?(?:\(|function\b|[A-Za-z_$][\w$]*\s*=>)/.test(arg);
}