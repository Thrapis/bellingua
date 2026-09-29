"use client";

import { useQuery } from "@tanstack/react-query";
import { ChevronRight, FileText, Folder, FolderOpen } from "lucide-react";
import { memo, useState } from "react";
import { api, type Counts, type TreeNode } from "@/lib/api";
import { cn } from "@/lib/utils";
import { CountsBar, donePct } from "@/components/pieces";

export type Scope = { file?: number; dir?: string };

type Props = {
  project: number;
  scope: Scope;
  onScope: (s: Scope) => void;
};

/** Lazy folder tree: each folder loads its children when first expanded. */
export function FileTree({ project, scope, onScope }: Props) {
  const root = useQuery({ queryKey: ["tree", project, ""], queryFn: () => api.tree(project, "") });
  const all = !scope.file && !scope.dir;
  return (
    <div className="flex flex-col py-1 text-sm">
      <button
        type="button"
        onClick={() => onScope({})}
        className={cn("mx-1 flex items-center gap-2 rounded-md px-2 py-1 text-left hover:bg-muted", all && "bg-muted font-medium")}
      >
        <FolderOpen className="size-4 text-muted-foreground" />
        All files
      </button>
      {root.data?.children.map((n) => (
        <Node key={n.path} node={n} depth={0} project={project} scope={scope} onScope={onScope} />
      ))}
    </div>
  );
}

const Node = memo(function Node({
  node,
  depth,
  project,
  scope,
  onScope,
}: {
  node: TreeNode;
  depth: number;
} & Props) {
  const isFile = !!node.file;
  // Expand ancestors of the current scope on first render.
  const scopePath = scope.dir ?? "";
  const [open, setOpen] = useState(() => !isFile && scopePath.startsWith(node.path + "/"));
  const kids = useQuery({
    queryKey: ["tree", project, node.path],
    queryFn: () => api.tree(project, node.path),
    enabled: open && !isFile,
  });
  const selected = isFile ? scope.file === node.file : !scope.file && scope.dir === node.path;
  const Icon = isFile ? FileText : open ? FolderOpen : Folder;

  return (
    <>
      <div
        className={cn(
          "group mx-1 flex cursor-pointer items-center gap-1 rounded-md py-0.5 pr-2 hover:bg-muted",
          selected && "bg-muted font-medium",
        )}
        style={{ paddingLeft: depth * 12 + 4 }}
        onClick={() => {
          if (isFile) onScope({ file: node.file });
          else {
            onScope({ dir: node.path });
            setOpen(true);
          }
        }}
        title={node.path}
      >
        <button
          type="button"
          className={cn("grid size-4 place-items-center text-muted-foreground", isFile && "invisible")}
          onClick={(e) => {
            e.stopPropagation();
            setOpen((o) => !o);
          }}
        >
          <ChevronRight className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        </button>
        <Icon className="size-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate">{node.name}</span>
        <Pct counts={node.counts} />
      </div>
      {open &&
        kids.data?.children.map((n) => (
          <Node key={n.path} node={n} depth={depth + 1} project={project} scope={scope} onScope={onScope} />
        ))}
    </>
  );
});

function Pct({ counts }: { counts: Counts }) {
  return (
    <span className="flex w-14 shrink-0 flex-col items-end gap-0.5">
      <span className="text-[10px] leading-none tabular-nums text-muted-foreground">
        {counts.errors > 0 && <span className="mr-1 text-destructive">{counts.errors}!</span>}
        {donePct(counts)}%
      </span>
      <CountsBar counts={counts} className="h-1" />
    </span>
  );
}
