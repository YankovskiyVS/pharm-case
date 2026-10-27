import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, api } from "../api-client";
import { ErrorBox } from "./common";
export type PageResult<T> = {
  items: T[];
  total: number;
  next_cursor: string | null;
  unread_count?: number;
};
export function params(
  path: string,
  values: Record<string, string | undefined>,
) {
  const [base, query] = path.split("?");
  const p = new URLSearchParams(query);
  for (const [key, value] of Object.entries(values)) {
    if (value) p.set(key, value);
    else p.delete(key);
  }
  return `${base}?${p}`;
}
export function usePages<T>(
  path: string | null,
  olderFirst = false,
  itemsKey: "items" | "recordings" = "items",
) {
  const [items, setItems] = useState<T[]>([]);
  const [total, setTotal] = useState(0);
  const [unread, setUnread] = useState(0);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [moreLoading, setMoreLoading] = useState(false);
  const [error, setError] = useState("");
  const [accessDenied, setAccessDenied] = useState(false);
  const [version, setVersion] = useState(0);
  const generation = useRef(0),
    pageCount = useRef(1),
    previousPath = useRef<string | null>(null);
  const clearDenied = useCallback(() => {
    generation.current++;
    pageCount.current = 1;
    setItems([]);
    setTotal(0);
    setUnread(0);
    setCursor(null);
    setAccessDenied(true);
  }, []);
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  useEffect(() => {
    window.addEventListener("data-refresh", reload);
    const timer = setInterval(reload, 15000);
    return () => {
      window.removeEventListener("data-refresh", reload);
      clearInterval(timer);
    };
  }, [reload]);
  function extract(page: PageResult<T>) {
    return (
      (itemsKey === "recordings"
        ? (page as PageResult<T> & { recordings?: T[] }).recordings
        : page.items) ?? []
    );
  }
  useEffect(() => {
    const controller = new AbortController();
    generation.current++;
    const run = generation.current;
    setError("");
    if (path !== previousPath.current) {
      pageCount.current = 1;
      previousPath.current = path;
      setAccessDenied(false);
      setItems([]);
      setCursor(null);
      setTotal(0);
    }
    if (!path) {
      setLoading(false);
      return;
    }
    setLoading(true);
    async function load() {
      const targetPages = pageCount.current;
      let all: T[] = [];
      let next: string | null = null;
      let last: PageResult<T> | null = null;
      for (let i = 0; i < targetPages; i++) {
        const page: PageResult<T> = await api<PageResult<T>>(
          params(path!, { limit: "25", cursor: next ?? undefined }),
          undefined,
          controller.signal,
        );
        const incoming =
          (itemsKey === "recordings"
            ? (page as PageResult<T> & { recordings?: T[] }).recordings
            : page.items) ?? [];
        all = olderFirst ? [...incoming, ...all] : [...all, ...incoming];
        last = page;
        next = page.next_cursor;
        if (!next) break;
      }
      if (controller.signal.aborted || run !== generation.current || !last)
        return;
      setAccessDenied(false);
      setItems(all);
      setTotal(last.total);
      setUnread(last.unread_count ?? 0);
      setCursor(next);
    }
    void load()
      .catch((e) => {
        if (controller.signal.aborted || run !== generation.current) return;
        if (e.name !== "AbortError") {
          if (e instanceof APIError && (e.status === 401 || e.status === 403))
            clearDenied();
          setError(e.message);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [path, version, itemsKey, olderFirst, clearDenied]);
  async function more() {
    if (!cursor || !path || moreLoading) return;
    setMoreLoading(true);
    setError("");
    const run = generation.current;
    try {
      const page: PageResult<T> = await api<PageResult<T>>(
        params(path, { cursor, limit: "25" }),
      );
      if (run !== generation.current) return;
      const incoming = extract(page);
      setItems((prev) =>
        olderFirst ? [...incoming, ...prev] : [...prev, ...incoming],
      );
      pageCount.current++;
      setCursor(page.next_cursor);
      setTotal(page.total);
      setUnread(page.unread_count ?? unread);
    } catch (e) {
      if (run === generation.current) {
        if (e instanceof APIError && (e.status === 401 || e.status === 403))
          clearDenied();
        setError((e as Error).message);
      }
    } finally {
      setMoreLoading(false);
    }
  }
  return {
    items,
    total,
    unread,
    cursor,
    loading,
    moreLoading,
    error,
    accessDenied,
    more,
    reload,
  };
}
export function PageControls({
  total,
  shown,
  cursor,
  loading,
  onMore,
}: {
  total: number;
  shown: number;
  cursor: string | null;
  loading: boolean;
  onMore: () => void;
}) {
  return (
    <div className="page-controls">
      <span className="muted">
        Показано {shown} из {total}
      </span>
      {cursor ? (
        <button className="secondary" disabled={loading} onClick={onMore}>
          {loading ? "Загружаем…" : "Показать ещё"}
        </button>
      ) : null}
    </div>
  );
}
export function QueryFailure({
  error,
  retry,
}: {
  error: string;
  retry: () => void;
}) {
  return error ? (
    <div>
      <ErrorBox message={error} />
      <button className="secondary" onClick={retry}>
        Повторить загрузку
      </button>
    </div>
  ) : null;
}
