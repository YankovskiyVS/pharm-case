import {
  Component,
  useState,
  useEffect,
  useRef,
  type FormEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import {
  Activity,
  ArrowRight,
  LogOut,
  Settings,
  ShieldCheck,
  AlertCircle,
  Check,
  X,
} from "lucide-react";
import { endpoint } from "../api-client";
import { useSession } from "./session";
import { confirmDiscard } from "./navigation";
import { AccountSettings, PasswordRecovery } from "./settings";
export function Brand() {
  return (
    <a
      className="brand"
      href={location.pathname.startsWith("/doctor") ? "/doctor/" : "/patient/"}
      aria-label="ПсиTech, главная"
    >
      <span className="brand-mark">
        <Activity size={22} />
      </span>
      <span>
        Пси<span className="brand-light">Tech</span>
      </span>
    </a>
  );
}
export function ErrorBox({ message }: { message: string }) {
  return message ? (
    <div className="error-box" role="alert">
      <AlertCircle size={18} />
      <span>{message}</span>
    </div>
  ) : null;
}
export function Notice({ children }: { children: ReactNode }) {
  return (
    <p className="notice" role="status">
      <Check size={17} />
      {children}
    </p>
  );
}
export function Loading() {
  return (
    <div className="loading" role="status">
      Загружаем ваш кабинет…
    </div>
  );
}
export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}
export function Login({ portal }: { portal: "doctor" | "patient" }) {
  const { login, error: connectionError } = useSession();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [invitation, setInvitation] = useState(() =>
    new URLSearchParams(location.hash.slice(1)).get("invite"),
  );
  const [accepted, setAccepted] = useState(false);
  const [recover, setRecover] = useState(false);
  const [resetToken, setResetToken] = useState(() =>
    new URLSearchParams(location.hash.slice(1)).get("reset"),
  );
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    try {
      if (invitation) {
        await endpoint("/auth/accept-invitation", "post", {
          body: {
            token: invitation,
            password: String(form.get("password")),
            consent: form.get("consent") === "on",
          },
        });
        setInvitation(null);
        history.replaceState(null, "", location.pathname);
        setAccepted(true);
      } else
        await login(
          String(form.get("email")),
          String(form.get("password")),
          String(form.get("code") ?? ""),
          portal,
        );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (recover || resetToken)
    return (
      <PasswordRecovery
        portal={portal}
        token={resetToken}
        onBack={() => {
          setRecover(false);
          setResetToken(null);
          history.replaceState(history.state, "", location.pathname);
        }}
      />
    );

  return (
    <div className={`login-page ${portal}`}>
      <div className="login-story">
        <Brand />
        <div>
          <span className="story-line" />
          <h1>
            {portal === "doctor"
              ? "Больше внимания.\nБлиже к пациенту."
              : "Маленькие записи.\nВажная забота о себе."}
          </h1>
          <p>
            {portal === "doctor"
              ? "Дневники, динамика и связь с пациентом в одном рабочем пространстве."
              : "Замечайте, как меняется ваше самочувствие, и оставайтесь на связи с врачом."}
          </p>
        </div>
        <span className="story-footer">
          Платформа наблюдения за психическим здоровьем
        </span>
      </div>
      <main className="login-main">
        <div className="login-card">
          <span className="portal-label">
            <ShieldCheck size={18} />
            {portal === "doctor" ? "Кабинет врача" : "Личный кабинет пациента"}
          </span>
          <h2>{invitation ? "Добро пожаловать" : "Рады вас видеть"}</h2>
          <p className="muted">
            {invitation
              ? "Задайте пароль для входа по приглашению врача."
              : "Войдите, чтобы продолжить."}
          </p>
          {accepted ? (
            <Notice>
              Регистрация завершена. Войдите с вашей почтой и новым паролем.
            </Notice>
          ) : null}
          <form onSubmit={submit}>
            {!invitation ? (
              <label>
                Электронная почта
                <input
                  name="email"
                  type="email"
                  autoComplete="username"
                  placeholder={
                    portal === "doctor"
                      ? "doctor@psitech.local"
                      : "patient@psitech.local"
                  }
                  required
                />
              </label>
            ) : null}
            <label>
              Пароль
              <input
                name="password"
                type="password"
                autoComplete={invitation ? "new-password" : "current-password"}
                minLength={invitation ? 12 : 1}
                maxLength={72}
                required
              />
            </label>
            {portal === "doctor" ? (
              <label>
                Код аутентификатора
                <input
                  name="code"
                  inputMode="numeric"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  autoComplete="one-time-code"
                  placeholder="6 цифр"
                  required
                />
              </label>
            ) : null}
            {invitation ? (
              <label className="checkbox">
                <input name="consent" type="checkbox" required />Я согласен на
                обработку данных для локального тестирования платформы
              </label>
            ) : null}
            <ErrorBox message={error || connectionError} />
            <button className="primary full" disabled={busy}>
              {busy
                ? "Подождите…"
                : invitation
                  ? "Создать аккаунт"
                  : "Войти в кабинет"}
              <ArrowRight size={18} />
            </button>
          </form>
          {!invitation ? (
            <button className="text-button" onClick={() => setRecover(true)}>
              Не помню пароль
            </button>
          ) : null}
          <p className="login-hint">
            {portal === "patient"
              ? "Новый аккаунт создаётся по приглашению вашего врача."
              : "Доступ предоставляется клиникой. Вход защищён вторым фактором."}
          </p>
          <a
            className="portal-switch"
            href={portal === "doctor" ? "/patient/" : "/doctor/"}
          >
            {portal === "doctor"
              ? "Перейти в кабинет пациента"
              : "Перейти в кабинет врача"}
          </a>
        </div>
        <div className="local-note">
          Локальная версия · только синтетические данные
        </div>
      </main>
    </div>
  );
}
export function Account() {
  const [settings, setSettings] = useState(false);
  const { user, logout } = useSession();
  const [error, setError] = useState("");
  return (
    <div className="account">
      <span className="avatar">{initials(user?.name ?? "")}</span>
      <div>
        <strong>{user?.name}</strong>
        <small>{user?.role === "doctor" ? "Врач" : "Пациент"}</small>
      </div>
      <button
        className="icon-button"
        aria-label="Настройки аккаунта"
        onClick={() => setSettings(true)}
      >
        <Settings size={18} />
      </button>
      <button
        className="icon-button"
        aria-label="Выйти"
        onClick={() => {
          if (confirmDiscard()) logout().catch((e) => setError(e.message));
        }}
      >
        <LogOut size={18} />
      </button>
      {error ? <ErrorBox message={error} /> : null}
      {settings ? (
        <Modal title="Настройки аккаунта" onClose={() => setSettings(false)}>
          <AccountSettings />
        </Modal>
      ) : null}
    </div>
  );
}
export function initials(name: string) {
  return name
    .split(" ")
    .slice(0, 2)
    .map((p) => p[0])
    .join("");
}
export function shortDate(date: string) {
  return new Date(
    date.length === 10 ? date + "T12:00:00" : date,
  ).toLocaleDateString("ru-RU", { day: "numeric", month: "short" });
}
export function timeLabel(date: string) {
  return new Date(date).toLocaleString("ru-RU", {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}
export function Modal({
  title,
  children,
  onClose,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLElement>(null);
  const safeClose = () => {
    if (confirmDiscard()) onClose();
  };
  const close = useRef(safeClose);
  close.current = safeClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const keyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        close.current();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = dialog.current?.querySelectorAll<HTMLElement>(
        "button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),a[href]",
      );
      if (!focusable?.length) return;
      const first = focusable[0],
        last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", keyboard);
    return () => {
      document.body.style.overflow = overflow;
      document.removeEventListener("keydown", keyboard);
      previous?.focus();
    };
  }, []);
  return createPortal(
    <div className="modal-backdrop">
      <section
        ref={dialog}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="section-heading">
          <h2>{title}</h2>
          <button
            autoFocus
            className="icon-button"
            onClick={safeClose}
            aria-label="Закрыть"
          >
            <X />
          </button>
        </div>
        {children}
      </section>
    </div>,
    document.body,
  );
}
export class ErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? (
      <main className="fatal">
        <h1>Не удалось открыть страницу</h1>
        <p>Обновите страницу, чтобы попробовать снова.</p>
        <button className="primary" onClick={() => location.reload()}>
          Обновить
        </button>
      </main>
    ) : (
      this.props.children
    );
  }
}
export { TrendChart, SleepTrendChart } from "./charts";
