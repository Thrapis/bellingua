"use client";

import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { EditorState, Prec, RangeSetBuilder, StateEffect, StateField, type Extension } from "@codemirror/state";
import {
  Decoration,
  type DecorationSet,
  EditorView,
  keymap,
  placeholder as cmPlaceholder,
  WidgetType,
} from "@codemirror/view";
import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef } from "react";
import { chipLabel } from "@/lib/pieces";

export type TargetEditorHandle = {
  /** Replace the document (new unit): resets undo history, keeps the view. */
  load: (text: string, codes: string[]) => void;
  insert: (text: string) => void;
  focus: () => void;
  value: () => string;
  /** The selected text, or "" when the selection is empty. */
  selection: () => string;
};

export type EditorKeys = {
  save: () => void;
  approve: () => void;
  /** Apply the machine translation (the glossary variant when asked and present). */
  mt: (glossary?: boolean) => void;
  copySource: () => void;
  next: () => void;
  prev: () => void;
  insertCode: (n: number) => void;
  addTerm: () => void;
  applyTM: (n: number) => void;
};

class ChipWidget extends WidgetType {
  constructor(readonly code: string) {
    super();
  }
  eq(o: ChipWidget) {
    return o.code === this.code;
  }
  toDOM() {
    const el = document.createElement("span");
    el.className = "bl-chip";
    el.textContent = chipLabel(this.code);
    el.title = this.code;
    return el;
  }
  ignoreEvent() {
    return false;
  }
}

const setCodes = StateEffect.define<string[]>();

const codesField = StateField.define<string[]>({
  create: () => [],
  update: (v, tr) => {
    for (const e of tr.effects) if (e.is(setCodes)) return e.value;
    return v;
  },
});

/**
 * Marks every occurrence of a source code in the document as an atomic chip,
 * using the same longest-first, count-limited matching as splitTarget, so an
 * excess copy of a code stays visible as raw text.
 */
function buildChips(doc: string, codes: string[]): DecorationSet {
  const b = new RangeSetBuilder<Decoration>();
  if (!codes.length || !doc) return b.finish();
  const remaining = new Map<string, number>();
  for (const c of codes) if (c) remaining.set(c, (remaining.get(c) ?? 0) + 1);
  const sorted = [...remaining.keys()].sort((a, b) => b.length - a.length);
  // Only positions where some code can start are worth probing.
  const firsts = new Set(sorted.map((c) => c[0]));
  for (let i = 0; i < doc.length; ) {
    if (!firsts.has(doc[i])) {
      i++;
      continue;
    }
    let hit = "";
    for (const c of sorted) {
      if (remaining.get(c)! > 0 && doc.startsWith(c, i)) {
        hit = c;
        break;
      }
    }
    if (!hit) {
      i++;
      continue;
    }
    remaining.set(hit, remaining.get(hit)! - 1);
    b.add(i, i + hit.length, Decoration.replace({ widget: new ChipWidget(hit) }));
    i += hit.length;
  }
  return b.finish();
}

const chipsField = StateField.define<DecorationSet>({
  create: (s) => buildChips(s.doc.toString(), s.field(codesField, false) ?? []),
  update: (deco, tr) => {
    if (tr.docChanged || tr.effects.some((e) => e.is(setCodes))) {
      return buildChips(tr.state.doc.toString(), tr.state.field(codesField));
    }
    return deco;
  },
  provide: (f) => [EditorView.decorations.from(f), EditorView.atomicRanges.of((v) => v.state.field(f))],
});

type Props = {
  keys: React.RefObject<EditorKeys>;
  onChange: (text: string) => void;
  placeholder?: string;
  className?: string;
};

export const TargetEditor = forwardRef<TargetEditorHandle, Props>(function TargetEditor(
  { keys, onChange, placeholder, className },
  ref,
) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  useLayoutEffect(() => {
    onChangeRef.current = onChange;
  });

  const extensions = useRef<Extension[]>([]);

  useEffect(() => {
    const k = () => keys.current;
    const run = (fn: (k: EditorKeys) => void) => () => {
      fn(k());
      return true;
    };
    extensions.current = [
      codesField,
      chipsField,
      history(),
      EditorView.lineWrapping,
      cmPlaceholder(placeholder ?? ""),
      Prec.highest(
        keymap.of([
          { key: "Mod-Enter", run: run((k) => k.save()) },
          { key: "Mod-Shift-Enter", run: run((k) => k.approve()) },
          { key: "Mod-Shift-c", run: run((k) => k.copySource()) },
          { key: "Mod-Shift-g", run: run((k) => k.addTerm()) },
          ...Array.from({ length: 5 }, (_, i) => ({ key: `Alt-${i + 1}`, run: run((k) => k.applyTM(i)) })),
          { key: "Alt-ArrowDown", run: run((k) => k.next()) },
          { key: "Alt-ArrowUp", run: run((k) => k.prev()) },
          ...Array.from({ length: 9 }, (_, i) => ({ key: `Mod-${i + 1}`, run: run((k) => k.insertCode(i)) })),
        ]),
      ),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) onChangeRef.current(u.state.doc.toString());
      }),
    ];
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({ doc: "", extensions: extensions.current }),
    });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // The view lives for the component's lifetime; handlers are read via refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useImperativeHandle(
    ref,
    () => ({
      load(text, codes) {
        const v = view.current;
        if (!v) return;
        const state = EditorState.create({ doc: text, extensions: extensions.current });
        v.setState(state);
        v.dispatch({ effects: setCodes.of(codes), selection: { anchor: text.length } });
      },
      insert(text) {
        const v = view.current;
        if (!v) return;
        const { from, to } = v.state.selection.main;
        v.dispatch({ changes: { from, to, insert: text }, selection: { anchor: from + text.length }, scrollIntoView: true });
        v.focus();
      },
      focus() {
        view.current?.focus();
      },
      value() {
        return view.current?.state.doc.toString() ?? "";
      },
      selection() {
        const v = view.current;
        if (!v) return "";
        const { from, to } = v.state.selection.main;
        return v.state.sliceDoc(from, to);
      },
    }),
    [],
  );

  return <div ref={host} className={className} />;
});
