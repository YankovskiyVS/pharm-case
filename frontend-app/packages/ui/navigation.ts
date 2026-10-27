import { useEffect, useState } from "react";

const drafts = new Map<symbol, string>();
let routeIndex = Number(history.state?.psitechIndex ?? 0);
history.replaceState(
  { ...history.state, psitechIndex: routeIndex },
  "",
  location.href,
);
export function confirmDiscard() {
  return (
    drafts.size === 0 ||
    window.confirm("Есть несохранённые данные. Уйти и потерять черновик?")
  );
}
export function useDraftGuard(dirty: boolean, label = "Черновик") {
  const [id] = useState(() => Symbol(label));
  useEffect(() => {
    if (dirty) drafts.set(id, label);
    else drafts.delete(id);
    const unload = (event: BeforeUnloadEvent) => {
      if (drafts.size) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", unload);
    return () => {
      drafts.delete(id);
      window.removeEventListener("beforeunload", unload);
    };
  }, [dirty, id, label]);
}
window.addEventListener("popstate", (event) => {
  const next = Number(event.state?.psitechIndex ?? 0);
  if (!confirmDiscard()) {
    history.go(routeIndex - next);
    return;
  }
  routeIndex = next;
  window.dispatchEvent(new Event("route-change"));
});
export function navigate(values: Record<string, string | null>) {
  if (!confirmDiscard()) return false;
  const url = new URL(location.href);
  if (
    !("target_type" in values) &&
    ["page", "tab", "patient"].some((key) => key in values)
  ) {
    for (const key of ["target", "target_type", "entry"])
      url.searchParams.delete(key);
    url.hash = "";
  }

  for (const [key, value] of Object.entries(values)) {
    if (key === "anchor") {
      url.hash = value ?? "";
      continue;
    }
    if (value) url.searchParams.set(key, value);
    else url.searchParams.delete(key);
  }
  routeIndex++;
  history.pushState({ psitechIndex: routeIndex }, "", url);
  window.dispatchEvent(new Event("route-change"));
  return true;
}
export function useUrlState(name: string, fallback: string) {
  const read = () => new URLSearchParams(location.search).get(name) ?? fallback;
  const [value, setValue] = useState(read);
  useEffect(() => {
    const update = () =>
      setValue(new URLSearchParams(location.search).get(name) ?? fallback);
    window.addEventListener("route-change", update);
    return () => window.removeEventListener("route-change", update);
  }, [name, fallback]);
  return [value, (next: string | null) => navigate({ [name]: next })] as const;
}
