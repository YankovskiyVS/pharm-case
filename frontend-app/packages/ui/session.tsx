import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import {
  APIError,
  api,
  endpoint,
  setCSRF,
  type Model,
  type User,
} from "../api-client";
type SessionState = {
  user: User | null;
  loading: boolean;
  error: string;
  login: (
    email: string,
    password: string,
    code: string,
    portal: "doctor" | "patient",
  ) => Promise<void>;
  logout: () => Promise<void>;
  reloadUser: () => Promise<void>;
};
const SessionContext = createContext<SessionState | null>(null);
export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    endpoint("/me", "get", { signal: controller.signal })
      .then((s) => {
        setUser(s.user);
        setCSRF(s.csrf);
      })
      .catch((e) => {
        if (e.status !== 401 && e.name !== "AbortError") setError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    const expire = () => {
      setUser(null);
      setCSRF("");
    };
    window.addEventListener("session-expired", expire);
    return () => {
      controller.abort();
      window.removeEventListener("session-expired", expire);
    };
  }, []);
  useEffect(() => {
    if (!user) return;
    const stream = new EventSource("/api/v1/events");
    const refresh = () => window.dispatchEvent(new Event("data-refresh"));
    stream.addEventListener("refresh", refresh);
    return () => stream.close();
  }, [user]);
  async function login(
    email: string,
    password: string,
    code: string,
    portal: "doctor" | "patient",
  ) {
    const s = await endpoint("/auth/login", "post", {
      body: {
        email,
        password,
        code,
        portal,
      },
    });
    setCSRF(s.csrf);
    setUser(s.user);
    setError("");
  }
  async function reloadUser() {
    const s = await api<Model<"Session">>("/me");
    setUser(s.user);
    setCSRF(s.csrf);
  }
  async function logout() {
    await endpoint("/auth/logout", "post", {});
    setCSRF("");
    setUser(null);
  }
  return (
    <SessionContext value={{ user, loading, error, login, logout, reloadUser }}>
      {children}
    </SessionContext>
  );
}
export function useSession() {
  const value = useContext(SessionContext);
  if (!value) throw new Error("Session context missing");
  return value;
}
export function useQuery<T>(path: string | null) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [version, setVersion] = useState(0);
  const [loadedPath, setLoadedPath] = useState<string | null>(null);
  useEffect(() => {
    const refresh = () => setVersion((v) => v + 1);
    window.addEventListener("data-refresh", refresh);
    const timer = setInterval(refresh, 15000);
    return () => {
      window.removeEventListener("data-refresh", refresh);
      clearInterval(timer);
    };
  }, []);
  useEffect(() => {
    if (!path) {
      setData(null);
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    setError("");
    api<T>(path, undefined, controller.signal)
      .then((value) => {
        setData(value);
        setLoadedPath(path);
      })
      .catch((e) => {
        if (e.name !== "AbortError") {
          if (e instanceof APIError && (e.status === 401 || e.status === 403))
            setData(null);
          setError(e.message);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [path, version]);
  return {
    data: loadedPath === path ? data : null,
    error,
    loading: loading || Boolean(path && loadedPath !== path && !error),
    reload: () => setVersion((v) => v + 1),
  };
}
