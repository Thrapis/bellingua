// Run with: node --test web/lib/   (Node 22.18+ strips the types of diff.ts itself)
import assert from "node:assert/strict";
import { test } from "node:test";
import { diffWords } from "./diff.ts";

const kinds = (segs) => segs.map((s) => (s.old === undefined ? [s.kind, s.text] : [s.kind, s.text, s.old]));

// Rebuilding both versions from the segments proves no text is lost or invented.
const rebuild = (segs) => ({
  old: segs.map((s) => (s.kind === "same" ? s.text : s.kind === "removed" ? s.text : s.kind === "replaced" ? s.old : "")).join(""),
  new: segs.map((s) => (s.kind === "removed" ? "" : s.text)).join(""),
});

test("identical strings are one unchanged segment", () => {
  assert.deepEqual(kinds(diffWords("Карта доступу", "Карта доступу")), [["same", "Карта доступу"]]);
});

test("a first translation is all added", () => {
  assert.deepEqual(kinds(diffWords("", "Прывітанне")), [["added", "Прывітанне"]]);
});

test("a changed word is a replacement that carries the old wording", () => {
  assert.deepEqual(kinds(diffWords("Карта доступа «Группы К»", "Карта доступу «Групы К»")), [
    ["same", "Карта "],
    ["replaced", "доступу", "доступа"],
    ["same", " «"],
    ["replaced", "Групы", "Группы"],
    ["same", " К»"],
  ]);
});

test("an inserted word is added, a dropped word is removed", () => {
  assert.deepEqual(kinds(diffWords("адна дзверы", "адна вялікая дзверы")), [
    ["same", "адна "],
    ["added", "вялікая "],
    ["same", "дзверы"],
  ]);
  assert.deepEqual(kinds(diffWords("адна вялікая дзверы", "адна дзверы")), [
    ["same", "адна "],
    ["removed", "вялікая "],
    ["same", "дзверы"],
  ]);
});

test("neighbouring changed words join into one replacement across the space", () => {
  assert.deepEqual(kinds(diffWords("Гэта значыць так", "То-бок менавіта так")), [
    ["replaced", "То-бок менавіта", "Гэта значыць"],
    ["same", " так"],
  ]);
});

test("an apostrophe stays inside its word", () => {
  assert.deepEqual(kinds(diffWords("аб'ект тут", "аб'екты тут")), [
    ["replaced", "аб'екты", "аб'ект"],
    ["same", " тут"],
  ]);
});

test("segments always rebuild both versions", () => {
  const cases = [
    ["", ""],
    ["a b c", ""],
    ["Першы радок.\nДругі радок.", "Першы радок!\nТрэці  радок."],
    ["<b>Увага</b>: {0} з {1}", "<b>Увага</b> — {1} з {0}"],
    ["x".repeat(50), "y".repeat(70)],
  ];
  for (const [a, b] of cases) assert.deepEqual(rebuild(diffWords(a, b)), { old: a, new: b }, JSON.stringify([a, b]));
});

test("huge strings fall back to prefix/suffix and stay fast", () => {
  const a = Array.from({ length: 30000 }, (_, i) => `слова${i}`).join(" ");
  const b = a.replace("слова15000", "іншае").replace("слова15900 ", "");
  const t = performance.now();
  const segs = diffWords(a, b);
  assert.ok(performance.now() - t < 500, "diff of 30k words took too long");
  assert.deepEqual(rebuild(segs), { old: a, new: b });
  assert.equal(segs[0].kind, "same");
  assert.equal(segs.at(-1).kind, "same");
});
