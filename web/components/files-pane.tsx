"use client";

import { useEffect, useState } from "react";
import { FolderTree, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { FileTree, type Scope } from "@/components/file-tree";

const STORAGE_KEY = "bellingua:files-open";

// Open/closed is a per-browser preference.
function loadOpen(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) !== "0";
  } catch {
    return true;
  }
}

type Props = { project: number; scope: Scope; onScope: (s: Scope) => void };

/**
 * The file tree as a collapsible left pane. Collapsed, it is a thin rail
 * with an expand button and a dot when a file/folder scope is active.
 * Ctrl+B toggles it.
 */
export function FilesPane({ project, scope, onScope }: Props) {
  const [open, setOpen] = useState(() => (typeof window === "undefined" ? true : loadOpen()));

  const toggle = setOpen;

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, open ? "1" : "0");
    } catch {}
  }, [open]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "b") {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const scoped = !!(scope.file || scope.dir);

  if (!open) {
    return (
      <aside className="flex w-10 shrink-0 flex-col items-center gap-1 border-r py-2">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" onClick={() => toggle(true)} aria-label="Show files (Ctrl+B)">
              <PanelLeftOpen />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">Show files (Ctrl+B)</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-sm" onClick={() => toggle(true)} aria-label="Files" className="relative">
              <FolderTree />
              {scoped && <span className="absolute right-1 top-1 size-1.5 rounded-full bg-primary" />}
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">
            {scope.file ? "Filtered to one file" : scope.dir ? `Filtered to ${scope.dir}` : "All files"}
          </TooltipContent>
        </Tooltip>
      </aside>
    );
  }

  return (
    <aside className="flex w-72 shrink-0 flex-col border-r">
      <div className="flex items-center gap-2 border-b px-3 py-1.5 text-xs text-muted-foreground">
        <FolderTree className="size-3.5" />
        <span className="flex-1 font-medium uppercase tracking-wide">Files</span>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="ghost" size="icon-xs" onClick={() => toggle(false)} aria-label="Hide files (Ctrl+B)">
              <PanelLeftClose />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">Hide files (Ctrl+B)</TooltipContent>
        </Tooltip>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <FileTree project={project} scope={scope} onScope={onScope} />
      </div>
    </aside>
  );
}
