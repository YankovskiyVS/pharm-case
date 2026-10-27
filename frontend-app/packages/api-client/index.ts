import type { components } from "./schema";
export type Model<K extends keyof components["schemas"]> =
  components["schemas"][K];
export type User = Model<"User">;
export type Entry = Model<"Entry">;
export type Detail = Model<"Detail">;
export type Alert = Model<"Alert">;
export type EMA = Model<"EMA">;
export type CSD = Model<"CSD">;
export type Message = Model<"Message">;
export type DoctorDashboard = Model<"DoctorDashboard">;
export type PatientSummary = Model<"PatientSummary">;
let csrf = "";
export function setCSRF(value: string) {
  csrf = value;
}
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
  timeoutMs = path.endsWith("/audio/search") ? 90000 : 12000,
): Promise<T> {
  const controller = new AbortController();
  let timedOut = false;
  const cancel = () => controller.abort();
  if (signal?.aborted) controller.abort();
  signal?.addEventListener("abort", cancel, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  try {
    const response = await fetch(`/api/v1${path}`, {
      method: body === undefined ? "GET" : "POST",
      credentials: "same-origin",
      signal: controller.signal,
      headers:
        body === undefined
          ? {}
          : { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    let data: unknown;
    try {
      data = await response.json();
    } catch (error) {
      if (controller.signal.aborted) throw error;
      throw new APIError(
        response.status,
        "Сервер временно недоступен. Повторите действие позже.",
      );
    }
    if (!response.ok) {
      if (response.status === 401 && path !== "/auth/login")
        window.dispatchEvent(new Event("session-expired"));
      throw new APIError(
        response.status,
        typeof data === "object" && data !== null && "error" in data
          ? String(data.error)
          : "Не удалось выполнить действие",
      );
    }
    return data as T;
  } catch (error) {
    if (timedOut)
      throw new APIError(
        0,
        "Сервер не ответил вовремя. Результат отправки неизвестен; повторите действие, чтобы проверить или сохранить данные без дубликата.",
      );
    if (signal?.aborted) throw new DOMException("Запрос отменён", "AbortError");
    if (error instanceof APIError) throw error;
    throw new APIError(
      0,
      "Нет связи с сервером. Проверьте подключение и повторите действие.",
    );
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", cancel);
  }
}

export function refreshData() {
  window.dispatchEvent(new Event("data-refresh"));
}

export function uploadAudio(
  blob: Blob,
  task: string,
  device: string,
  key: string,
  options: {
    signal?: AbortSignal;
    onProgress?: (percent: number) => void;
    timeoutMs?: number;
  } = {},
): Promise<{ id: string }> {
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    const query = new URLSearchParams({ task, device, key });
    request.open("POST", `/api/v1/patient/audio?${query}`);
    request.timeout = options.timeoutMs ?? 120000;
    request.setRequestHeader("Content-Type", blob.type || "audio/wav");
    request.setRequestHeader("X-CSRF-Token", csrf);
    request.setRequestHeader("X-Audio-Consent", "audio-v1");
    const cancel = () => request.abort();
    const cleanup = () => options.signal?.removeEventListener("abort", cancel);
    options.signal?.addEventListener("abort", cancel, { once: true });
    request.upload.onprogress = (event) => {
      if (event.lengthComputable)
        options.onProgress?.(Math.round((event.loaded / event.total) * 100));
    };
    request.onerror = () => {
      cleanup();
      reject(
        new APIError(
          0,
          "Нет связи. Черновик сохранён на странице; повторите отправку.",
        ),
      );
    };
    request.ontimeout = () => {
      cleanup();
      reject(
        new APIError(
          0,
          "Истекло время загрузки. Результат неизвестен; повторите с тем же черновиком без создания дубликата.",
        ),
      );
    };
    request.onabort = () => {
      cleanup();
      reject(
        new DOMException("Загрузка отменена. Черновик сохранён.", "AbortError"),
      );
    };
    request.onload = () => {
      cleanup();
      if (request.status === 401)
        window.dispatchEvent(new Event("session-expired"));
      let data: unknown;
      try {
        data = JSON.parse(request.responseText);
      } catch {
        reject(
          new APIError(
            request.status,
            "Сервер не подтвердил сохранение. Повторите отправку.",
          ),
        );
        return;
      }
      if (request.status < 200 || request.status >= 300) {
        reject(
          new APIError(
            request.status,
            typeof data === "object" && data !== null && "error" in data
              ? String(data.error)
              : "Не удалось отправить запись",
          ),
        );
        return;
      }
      if (
        typeof data !== "object" ||
        data === null ||
        !("id" in data) ||
        typeof data.id !== "string"
      ) {
        reject(
          new APIError(request.status, "Некорректное подтверждение сохранения"),
        );
        return;
      }
      resolve({ id: data.id });
    };
    if (options.signal?.aborted) {
      reject(new DOMException("Загрузка отменена", "AbortError"));
      return;
    }
    request.send(blob);
  });
}

export async function downloadReport(path: string, filename: string) {
  const response = await fetch(`/api/v1${path}`, {
    credentials: "same-origin",
  });
  if (!response.ok)
    throw new APIError(response.status, "Не удалось получить полный отчёт");
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

// Endpoint paths, parameters and request/response bodies are checked against OpenAPI.
// The low-level api transport remains internal to variable-path pagination helpers.
type Paths = import("./schema").paths;
type Method<P extends keyof Paths> = {
  [M in "get" | "post"]: M extends keyof Paths[P]
    ? NonNullable<Paths[P][M]> extends never
      ? never
      : M
    : never;
}["get" | "post"];
type Operation<
  P extends keyof Paths,
  M extends Method<P>,
> = M extends keyof Paths[P] ? NonNullable<Paths[P][M]> : never;
type Body<O> = O extends {
  requestBody: { content: { "application/json": infer B } };
}
  ? B
  : never;
type PathOptions<O> = O extends { parameters: { path: infer P } }
  ? [P] extends [never]
    ? { path?: never }
    : { path: P }
  : { path?: never };
type QueryOptions<O> = O extends { parameters: { query?: infer Q } }
  ? { query?: NonNullable<Q> }
  : { query?: never };
type BodyOptions<O> = [Body<O>] extends [never]
  ? { body?: never }
  : { body: Body<O> };
type ResponseOf<O> = O extends { responses: infer R }
  ? {
      [K in keyof R]: K extends 200 | 201 | 202
        ? R[K] extends { content: { "application/json": infer V } }
          ? V
          : never
        : never;
    }[keyof R]
  : never;
export function endpoint<P extends keyof Paths, M extends Method<P>>(
  path: P,
  method: M,
  options: PathOptions<Operation<P, M>> &
    QueryOptions<Operation<P, M>> &
    BodyOptions<Operation<P, M>> & { signal?: AbortSignal; timeoutMs?: number },
): Promise<ResponseOf<Operation<P, M>>> {
  let resolved: string = path;
  if (options.path)
    for (const [key, value] of Object.entries(options.path))
      resolved = resolved.replace(
        `{${key}}`,
        encodeURIComponent(String(value)),
      );
  if (resolved.includes("{"))
    throw new Error("Missing endpoint path parameter");
  if (options.query) {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(options.query)) {
      if (value !== undefined && value !== null) query.set(key, String(value));
    }
    resolved += `?${query}`;
  }
  return api<ResponseOf<Operation<P, M>>>(
    resolved,
    method === "post" ? (options.body ?? {}) : undefined,
    options.signal,
    options.timeoutMs,
  );
}
