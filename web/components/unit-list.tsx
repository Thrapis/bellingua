"use client";

import { useVirtualizer } from "@tanstack/react-virtual";
import { AlertTriangle, CircleAlert, RefreshCw } from "lucide-react";
import { memo, useEffect, useRef } from "react";
import type { Row } from "@/lib/api";
import { cn } from "@/lib/utils";
import { PiecesView, StateDot } from "@/components/pieces";

type Props = {
  rows: Row[];
  selectedId: number | null;
  errMask: number;
  drafts: ReadonlySet<number>;
  hasNext: boolean;
  loading: boolean;
  onSelect: (id: number) => void;
  onNeedMore: () => void;
};

/**
 * Virtualized list of unit previews. Only the visible rows (plus overscan)
 * are in the DOM, so 180k rows scroll like 20. Rows are measured after
 * render; the estimate only has to be close.
 */
export function UnitList({ rows, selectedId, errMask, drafts, hasNext, loading, onSelect, onNeedMore }: Props) {
  const scroller = useRef<HTMLDivElement>(null);
  const count = rows.length + (hasNext ? 1 : 0);
  // eslint-disable-next-line react-hooks/incompatible-library
  const v = useVirtualizer({
    count,
    getScrollElement: () => scroller.current,
    estimateSize: () => 58,
    overscan: 10,
    getItemKey: (i) => rows[i]?.id ?? "more",
  });
  const items = v.getVirtualItems();

  // Load the next page when the end is within two screens.
  const lastIndex = items.length ? items[items.length - 1].index : 0;
  useEffect(() => {
    if (hasNext && !loading && lastIndex >= rows.length - 40) onNeedMore();
  }, [lastIndex, rows.length, hasNext, loading, onNeedMore]);

  // Keep the selection in view (keyboard navigation).
  const selectedIndex = selectedId == null ? -1 : rows.findIndex((r) => r.id === selectedId);
  useEffect(() => {
    if (selectedIndex >= 0) v.scrollToIndex(selectedIndex, { align: "auto" });
  }, [selectedIndex, v]);

  return (
    <div ref={scroller} className="h-full overflow-y-auto overscroll-contain" tabIndex={-1}>
      <div style={{ height: v.getTotalSize(), position: "relative" }}>
        {items.map((it) => {
          const row = rows[it.index];
          return (
            <div
              key={it.key}
              data-index={it.index}
              ref={v.measureElement}
              style={{ position: "absolute", top: 0, left: 0, right: 0, transform: `translateY(${it.start}px)` }}
            >
              {row ? (
                <UnitRow
                  row={row}
                  selected={row.id === selectedId}
                  draft={drafts.has(row.id)}
                  errMask={errMask}
                  onSelect={onSelect}
                />
              ) : (
                <div className="px-4 py-3 text-sm text-muted-foreground">Loading…</div>
              )}
            </div>
          );
        })}
      </div>
      {!loading && rows.length === 0 && (
        <div className="p-8 text-center text-sm text-muted-foreground">No strings match the filter.</div>
      )}
    </div>
  );
}

const UnitRow = memo(function UnitRow({
  row,
  selected,
  draft,
  errMask,
  onSelect,
}: {
  row: Row;
  selected: boolean;
  draft: boolean;
  errMask: number;
  onSelect: (id: number) => void;
}) {
  const hasErr = (row.qa & errMask) !== 0;
  const hasWarn = !hasErr && row.qa !== 0;
  return (
    <div
      onClick={() => onSelect(row.id)}
      className={cn(
        "grid cursor-pointer grid-cols-[auto_1fr_1fr] gap-x-3 border-b border-l-2 border-l-transparent px-3 py-2 text-sm hover:bg-muted/60",
        selected && "border-l-primary bg-muted",
      )}
    >
      <div className="flex flex-col items-center gap-1.5 pt-1">
        <StateDot state={row.state} />
        {hasErr && <CircleAlert className="size-3.5 text-destructive" />}
        {hasWarn && <AlertTriangle className="size-3.5 text-amber-500" />}
        {row.changed && <RefreshCw className="size-3.5 text-sky-500" aria-label="Source changed" />}
      </div>
      <div className="min-w-0">
        <div className="line-clamp-3">
          <PiecesView pieces={row.src} />
        </div>
        <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
          {row.key}
          {row.trunc && ` · ${row.srcLen.toLocaleString()} chars`}
        </div>
        {row.context && (
          <div className="truncate text-[10px] text-muted-foreground/80" title={row.context}>
            {row.context}
          </div>
        )}
      </div>
      <div className="min-w-0">
        {row.tgt ? (
          <div className={cn("line-clamp-3", row.state === "mt" && "text-muted-foreground")}>
            <PiecesView pieces={row.tgt} />
          </div>
        ) : (
          <span className="italic text-muted-foreground/60">not translated</span>
        )}
        {draft && <div className="mt-0.5 text-[10px] font-medium text-amber-600">unsaved draft</div>}
      </div>
    </div>
  );
});
