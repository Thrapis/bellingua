"use client";

import { useState } from "react";
import type { LucideIcon } from "lucide-react";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { cn } from "@/lib/utils";

const STORAGE_KEY = "bellingua:panel-open";
const DEFAULT_OPEN = ["tm", "mt", "glossary", "qa"];

// Which sections are open is a per-browser preference.
function loadOpen(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null");
    return Array.isArray(v) ? v : DEFAULT_OPEN;
  } catch {
    return DEFAULT_OPEN;
  }
}

/**
 * The open sections, remembered per browser. Owned by the panel, which also
 * acts on it (an open Machine translation section translates on its own).
 */
export function usePanelOpen() {
  const [open, setOpen] = useState<string[]>(() => (typeof window === "undefined" ? DEFAULT_OPEN : loadOpen()));
  const change = (v: string[]) => {
    setOpen(v);
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(v));
    } catch {}
  };
  return [open, change] as const;
}

/** Collapsible sections of the unit panel (Crowdin-style). */
export function PanelSections({
  open,
  onOpenChange,
  children,
}: {
  open: string[];
  onOpenChange: (v: string[]) => void;
  children: React.ReactNode;
}) {
  return (
    <Accordion type="multiple" value={open} onValueChange={onOpenChange}>
      {children}
    </Accordion>
  );
}

type Tone = "default" | "good" | "warn" | "error";

const toneClass: Record<Tone, string> = {
  default: "bg-muted text-muted-foreground",
  good: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
  warn: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  error: "bg-destructive/15 text-destructive",
};

type SectionProps = {
  value: string;
  icon: LucideIcon;
  title: string;
  /** Shown as a pill next to the title; omitted while unknown. */
  count?: number;
  tone?: Tone;
  /** Buttons at the right of the header (outside the toggle). */
  actions?: React.ReactNode;
  children: React.ReactNode;
};

export function PanelSection({ value, icon: Icon, title, count, tone = "default", actions, children }: SectionProps) {
  return (
    <AccordionItem value={value} className="border-b">
      {/* The trigger's <h3> grows so the chevron sits at the right, before the actions. */}
      <div className="flex items-center gap-1 pr-2 [&>h3]:flex-1">
        <AccordionTrigger className="gap-2 rounded-none px-4 py-2.5 text-xs text-muted-foreground hover:bg-muted/50 hover:no-underline">
          <span className="flex items-center gap-2">
            <Icon className="size-3.5" />
            <span className="font-medium uppercase tracking-wide">{title}</span>
            {count !== undefined && (
              <span
                className={cn(
                  "rounded-full px-1.5 text-[10px] font-semibold tabular-nums leading-4",
                  count ? toneClass[tone] : toneClass.default,
                )}
              >
                {count}
              </span>
            )}
          </span>
        </AccordionTrigger>
        {actions}
      </div>
      <AccordionContent className="px-4 pt-1.5 pb-3">{children}</AccordionContent>
    </AccordionItem>
  );
}

/** Muted one-liner for an empty section. */
export function Empty({ children }: { children: React.ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>;
}
