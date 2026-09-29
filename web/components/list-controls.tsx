"use client";

import { ArrowUpDown, ListFilter, X } from "lucide-react";
import { STATES, SORTS, type Filter, type QACheck, type Sort, type State } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { StateDot, stateLabel } from "@/components/pieces";

/** URL patch: undefined removes a parameter. */
export type FilterPatch = Partial<Record<"in" | "state" | "qa" | "changed" | "same", string | undefined>>;

const IN_LABELS: Record<NonNullable<Filter["in"]>, string> = {
  both: "Source + translation",
  source: "Source",
  target: "Translation",
  key: "Key",
  context: "Context",
};

function qaLabel(qa: string, checks: QACheck[]) {
  if (qa === "any") return "Any QA issue";
  if (qa === "placeholders,empty") return "QA errors";
  return checks.find((c) => c.id === qa)?.title ?? qa;
}

type Props = { filter: Filter; qaChecks: QACheck[]; onChange: (p: FilterPatch) => void };

/** The filters behind one button, plus removable chips for what is active. */
export function FilterMenu({ filter, qaChecks, onChange }: Props) {
  const states = filter.states ?? [];
  const inField = filter.in ?? "both";
  const active =
    (inField !== "both" ? 1 : 0) + (states.length ? 1 : 0) + (filter.qa ? 1 : 0) + (filter.changed ? 1 : 0) + (filter.same ? 1 : 0);
  const setStates = (s: State[]) => onChange({ state: s.length ? s.join(",") : undefined });

  return (
    <>
      <Popover>
        <PopoverTrigger asChild>
          <Button size="sm" variant={active ? "secondary" : "outline"} className="gap-1.5">
            <ListFilter /> Filters
            {active > 0 && (
              <span className="grid size-4 place-items-center rounded-full bg-primary text-[10px] text-primary-foreground">{active}</span>
            )}
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="grid w-80 gap-4">
          <div className="grid gap-1.5">
            <Label className="text-xs text-muted-foreground">Search in</Label>
            <ToggleGroup
              type="single"
              size="sm"
              variant="outline"
              className="flex-wrap justify-start"
              value={inField}
              onValueChange={(v) => v && onChange({ in: v === "both" ? undefined : v })}
            >
              {(Object.keys(IN_LABELS) as NonNullable<Filter["in"]>[]).map((k) => (
                <ToggleGroupItem key={k} value={k} className="px-2 text-xs">
                  {k === "both" ? "Both" : IN_LABELS[k]}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>

          <div className="grid gap-1.5">
            <Label className="text-xs text-muted-foreground">State</Label>
            <div className="grid grid-cols-2 gap-1.5">
              {STATES.map((s) => (
                <label key={s} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={states.includes(s)}
                    onCheckedChange={(v) => setStates(v ? [...states, s] : states.filter((x) => x !== s))}
                  />
                  <StateDot state={s} /> {stateLabel[s]}
                </label>
              ))}
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label className="text-xs text-muted-foreground">QA</Label>
            <Select value={filter.qa ?? "none"} onValueChange={(v) => onChange({ qa: v === "none" ? undefined : v })}>
              <SelectTrigger size="sm" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">Any (no QA filter)</SelectItem>
                <SelectItem value="any">Any QA issue</SelectItem>
                <SelectItem value="placeholders,empty">QA errors</SelectItem>
                <SelectSeparator />
                {qaChecks.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={!!filter.changed} onCheckedChange={(v) => onChange({ changed: v ? "1" : undefined })} />
            Source changed in the last import
          </label>

          {active > 0 && (
            <Button
              size="sm"
              variant="ghost"
              className="justify-self-start"
              onClick={() => onChange({ in: undefined, state: undefined, qa: undefined, changed: undefined, same: undefined })}
            >
              <X /> Reset filters
            </Button>
          )}
        </PopoverContent>
      </Popover>

      {inField !== "both" && <Chip label={`In: ${IN_LABELS[inField]}`} onRemove={() => onChange({ in: undefined })} />}
      {states.length > 0 && (
        <Chip
          label={
            <span className="inline-flex items-center gap-1">
              {states.map((s) => (
                <StateDot key={s} state={s} />
              ))}
              {states.length === 1 ? stateLabel[states[0]] : `${states.length} states`}
            </span>
          }
          onRemove={() => setStates([])}
        />
      )}
      {filter.qa && <Chip label={qaLabel(filter.qa, qaChecks)} onRemove={() => onChange({ qa: undefined })} />}
      {filter.changed && <Chip label="Source changed" onRemove={() => onChange({ changed: undefined })} />}
      {filter.same && <Chip label="Identical source" onRemove={() => onChange({ same: undefined })} />}
    </>
  );
}

function Chip({ label, onRemove }: { label: React.ReactNode; onRemove: () => void }) {
  return (
    <span className="inline-flex h-7 items-center gap-1 rounded-full border pl-2.5 pr-1 text-xs">
      {label}
      <button type="button" onClick={onRemove} className="grid size-5 place-items-center rounded-full hover:bg-muted" aria-label="Remove filter">
        <X className="size-3" />
      </button>
    </span>
  );
}

export function SortMenu({ sort, onChange }: { sort: Sort; onChange: (s: Sort) => void }) {
  const current = SORTS.find((s) => s.id === sort) ?? SORTS[0];
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button size="sm" variant={sort === "file" ? "outline" : "secondary"} className="gap-1.5" title="Sort">
          <ArrowUpDown /> <span className="max-w-36 truncate">{current.label}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Sort strings by</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={sort} onValueChange={(v) => onChange(v as Sort)}>
          {SORTS.map((s) => (
            <DropdownMenuRadioItem key={s.id} value={s.id}>
              {s.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
