const names = (() => {
  try {
    return new Intl.DisplayNames(["en"], { type: "language" });
  } catch {
    return null;
  }
})();

/** English name of an IETF language tag ("be" → "Belarusian"); the tag itself when unknown. */
export function langName(tag: string): string {
  try {
    return names?.of(tag) ?? tag;
  } catch {
    return tag;
  }
}
