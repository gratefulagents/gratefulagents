import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

/**
 * Declarative URL-backed filter state.
 *
 * Every list surface in the app keeps its search box, filters, sort and tab
 * selection in the query string so a filtered view is shareable, survives a
 * reload, and can be linked to from anywhere (overview tiles, notifications,
 * command palette). A key whose value equals its default is removed from the
 * URL, so the common case stays a clean `/security/runs`.
 *
 * Filter edits use `replace` by default: dragging a severity filter should not
 * bury the previous page under a pile of history entries. Pass
 * `{ history: "push" }` for state a user would expect the back button to undo
 * (for example the selected tab of a page).
 */
export type UrlFilterSpec = Record<string, string>;

export type UrlFilterValues<S extends UrlFilterSpec> = { [K in keyof S]: string };

export type SetOptions = { history?: "replace" | "push" };

export type UrlFilters<S extends UrlFilterSpec> = {
  /** Current value for every declared key (default when absent from the URL). */
  values: UrlFilterValues<S>;
  /** Set one key. Setting a key back to its default removes it from the URL. */
  set: (key: keyof S, value: string, options?: SetOptions) => void;
  /** Set several keys in a single navigation. */
  setMany: (patch: Partial<UrlFilterValues<S>>, options?: SetOptions) => void;
  /** Reset every declared key to its default, leaving unrelated params alone. */
  reset: (options?: SetOptions) => void;
  /** True when the key differs from its default. */
  isActive: (key: keyof S) => boolean;
  /** Number of non-default keys, ignoring keys listed in `ignore`. */
  activeCount: (ignore?: Array<keyof S>) => number;
  /** Query string (with leading `?`) carrying the current non-default values. */
  queryString: string;
};

/** Merge a patch into params, dropping keys that match their default. */
function applyPatch<S extends UrlFilterSpec>(
  params: URLSearchParams,
  spec: S,
  patch: Partial<UrlFilterValues<S>>,
): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const [key, value] of Object.entries(patch)) {
    if (value === undefined) continue;
    const fallback = spec[key];
    if (value === fallback || value === "") {
      next.delete(key);
    } else {
      next.set(key, value);
    }
  }
  return next;
}

export function useUrlFilters<S extends UrlFilterSpec>(spec: S): UrlFilters<S> {
  const [searchParams, setSearchParams] = useSearchParams();

  // `spec` is written inline at call sites, so identity changes every render.
  // Key the memo on its serialized shape instead of the object identity.
  const specKey = JSON.stringify(spec);
  const stableSpec = useMemo(() => spec, [specKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const values = useMemo(() => {
    const out = {} as UrlFilterValues<S>;
    for (const key of Object.keys(stableSpec)) {
      out[key as keyof S] = searchParams.get(key) ?? stableSpec[key];
    }
    return out;
  }, [searchParams, stableSpec]);

  const setMany = useCallback(
    (patch: Partial<UrlFilterValues<S>>, options?: SetOptions) => {
      setSearchParams(
        (current) => applyPatch(current, stableSpec, patch),
        { replace: options?.history !== "push" },
      );
    },
    [setSearchParams, stableSpec],
  );

  const set = useCallback(
    (key: keyof S, value: string, options?: SetOptions) => {
      setMany({ [key]: value } as Partial<UrlFilterValues<S>>, options);
    },
    [setMany],
  );

  const reset = useCallback(
    (options?: SetOptions) => {
      const cleared = {} as Partial<UrlFilterValues<S>>;
      for (const key of Object.keys(stableSpec)) {
        cleared[key as keyof S] = stableSpec[key];
      }
      setMany(cleared, options);
    },
    [setMany, stableSpec],
  );

  const isActive = useCallback(
    (key: keyof S) => values[key] !== stableSpec[key as string],
    [values, stableSpec],
  );

  const activeCount = useCallback(
    (ignore?: Array<keyof S>) =>
      Object.keys(stableSpec).filter(
        (key) =>
          !ignore?.includes(key as keyof S)
          && values[key as keyof S] !== stableSpec[key],
      ).length,
    [values, stableSpec],
  );

  const queryString = useMemo(() => {
    const params = applyPatch(new URLSearchParams(), stableSpec, values);
    const text = params.toString();
    return text ? `?${text}` : "";
  }, [values, stableSpec]);

  return { values, set, setMany, reset, isActive, activeCount, queryString };
}
