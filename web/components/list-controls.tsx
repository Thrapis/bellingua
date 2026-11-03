"use client";

import { ArrowUpDown, Languages, ListFilter, X } from "lucide-react";
import { STATES, SORTS, type Filter, type ProjectLang, type QACheck, type Sort, type State } from "@/lib/api";
import { langName } from "@/lib/lang";
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
import { StateDot, stateLabel } from "@/components/pieces";

/** URL patch: undefined removes a parameter. */
export type FilterPatch = Partial<Record<"in" | "state" | "qa" | "changed" | "same", string | undefined>>;

const FIELDS = [
  { id: "source", label: "Source" },
  { id: "target", label: "Translation" },
  { id: "key", label: "Key" },
  { id: "context", label: "Context" },
];
const FIELD_ORDER = FIELDS.map((f) => f.id);

/** The fields a filter searches in; no value means source and translation. */
function parseIn(v: string | undefined): string[] {
  const out = (v ?? "").split(",").flatMap((f) => (f === "both" ? ["source", "target"] : f ? [f] : []));
  return out.length ? out : ["source", "target"];
}

/** Back to the URL form: fixed order, and the default is no value at all. */
function joinIn(fields: string[], langs: string[]): string | undefined {
  const rank = (f: string) => (f.startsWith("lang:") ? FIELD_ORDER.length + langs.indexOf(f.slice(5)) : FIELD_ORDER.indexOf(f));
  const sorted = [...new Set(fields)].sort((x, y) => rank(x) - rank(y));
  return sorted.join(",") === "source,target" ? undefined : sorted.join(",");
}

function fieldLabel(f: string): string {
  return f.startsWith("lang:") ? langName(f.slice(5)) : (FIELDS.find((x) => x.id === f)?.label ?? f);
}

function qaLabel(qa: string, checks: QACheck[]) {
  if (qa === "any") return "Any QA issue";
  if (qa === "placeholders,empty") return "QA errors";
  return checks.find((c) => c.id === qa)?.title ?? qa;
}

type Props = { filter: Filter; qaChecks: QACheck[]; langs: ProjectLang[]; onChange: (p: FilterPatch) => void };

/** The filters behind one button, plus removable chips for what is active. */
export function FilterMenu({ filter, qaChecks, langs, onChange }: Props) {
  const states = filter.states ?? [];
  const inFields = parseIn(filter.in);
  const langCodes = langs.map((l) => l.lang);
  const setIn = (fields: string[]) => fields.length && onChange({ in: joinIn(fields, langCodes) });
  const active =
    (filter.in ? 1 : 0) + (states.length ? 1 : 0) + (filter.qa ? 1 : 0) + (filter.changed ? 1 : 0) + (filter.same ? 1 : 0);
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
            <Label className="text-xs text-muted-foreground">Search in (any of the ticked)</Label>
            <div className="grid grid-cols-2 gap-1.5">
              {FIELDS.map((f) => (
                <label key={f.id} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={inFields.includes(f.id)}
                    onCheckedChange={(v) => setIn(v ? [...inFields, f.id] : inFields.filter((x) => x !== f.id))}
                  />
                  {f.label}
                </label>
              ))}
            </div>
            {langCodes.length > 0 && (
              <div className="grid gap-1.5">
                <div className="grid grid-cols-2 gap-1.5">
                  {langCodes.map((l) => {
                    const id = `lang:${l}`;
                    return (
                      <label key={id} className="flex items-center gap-2 text-sm">
                        <Checkbox
                          checked={inFields.includes(id)}
                          onCheckedChange={(v) => setIn(v ? [...inFields, id] : inFields.filter((x) => x !== id))}
                        />
                        <span className="rounded bg-muted px-1 font-mono text-[10px] uppercase text-muted-foreground">{l}</span>
                        <span className="truncate">{langName(l)}</span>
                      </label>
                    );
                  })}
                </div>
              </div>
            )}
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

      {filter.in && (
        <Chip
          label={
            <span className="inline-flex items-center gap-1">
              In: {inFields.map(fieldLabel).join(", ")}
              {inFields.some((f) => f.startsWith("lang:")) && <Languages className="size-3 text-muted-foreground" />}
            </span>
          }
          onRemove={() => onChange({ in: undefined })}
        />
      )}
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
    <span className="inline-flex h-7 items-center gap-1 whitespace-nowrap rounded-full border pl-2.5 pr-1 text-xs">
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
