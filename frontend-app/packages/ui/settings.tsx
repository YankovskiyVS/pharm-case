import { useState, type FormEvent } from "react";
import { api, endpoint, refreshData } from "../api-client";
import { useQuery, useSession } from "./session";
import { ErrorBox, Notice, timeLabel } from "./common";
import { useDraftGuard } from "./navigation";
type SessionInfo = {
  id: string;
  created_at: string;
  expires_at: string;
  current: boolean;
};
export function AccountSettings() {
  const { user, reloadUser } = useSession();
  const [tab, setTab] = useState("profile");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const { data, reload } = useQuery<{ items: SessionInfo[] }>("/auth/sessions");
  useDraftGuard(dirty, "Настройки аккаунта");
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setNotice("");
    const f = new FormData(event.currentTarget);
    try {
      if (tab === "profile") {
        await endpoint("/me/profile", "post", {
          body: {
            name: String(f.get("name")),
            timezone: String(f.get("timezone")),
          },
        });
        setDirty(false);
        await reloadUser();
        setNotice("Профиль сохранён");
        refreshData();
      } else {
        await endpoint("/auth/password/change", "post", {
          body: {
            current_password: String(f.get("current_password")),
            new_password: String(f.get("new_password")),
            code: String(f.get("code") ?? ""),
          },
        });
        setDirty(false);
        window.dispatchEvent(new Event("session-expired"));
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function revoke(id?: string) {
    setError("");
    try {
      await api(
        id ? `/auth/sessions/${id}/revoke` : "/auth/sessions/revoke-others",
        {},
      );
      if (data?.items.find((s) => s.id === id)?.current)
        window.dispatchEvent(new Event("session-expired"));
      else {
        setNotice("Сессии завершены");
        reload();
      }
    } catch (e) {
      setError((e as Error).message);
    }
  }
  return (
    <>
      <div className="tabs" role="tablist" aria-label="Настройки">
        {[
          ["profile", "Профиль"],
          ["password", "Пароль"],
          ["sessions", "Сессии"],
        ].map(([key, label]) => (
          <button
            key={key}
            role="tab"
            aria-selected={tab === key}
            onClick={() => {
              if (
                !dirty ||
                window.confirm("Потерять несохранённые изменения?")
              ) {
                setDirty(false);
                setTab(key);
                setError("");
                setNotice("");
              }
            }}
          >
            {label}
          </button>
        ))}
      </div>
      <ErrorBox message={error} />
      {notice ? <Notice>{notice}</Notice> : null}
      {tab === "sessions" ? (
        <>
          <p className="helper">
            Завершите доступ на других устройствах. Учётные записи врачей
            создаёт оператор БД.
          </p>
          <button className="secondary" onClick={() => void revoke()}>
            Завершить другие сессии
          </button>
          {data?.items.map((s) => (
            <article className="schedule-row" key={s.id}>
              <div>
                <strong>
                  {s.current ? "Текущая сессия" : "Другая сессия"}
                </strong>
                <span>
                  Начало: {timeLabel(s.created_at)}. До:{" "}
                  {timeLabel(s.expires_at)}
                </span>
              </div>
              <button className="text-button" onClick={() => void revoke(s.id)}>
                Завершить
              </button>
            </article>
          ))}
        </>
      ) : (
        <form key={tab} onSubmit={save} onChange={() => setDirty(true)}>
          {tab === "profile" ? (
            <>
              <label>
                Имя
                <input
                  name="name"
                  defaultValue={user?.name}
                  minLength={2}
                  maxLength={120}
                  required
                />
              </label>
              <label>
                Часовой пояс
                <input
                  name="timezone"
                  defaultValue={user?.timezone}
                  placeholder="Europe/Moscow"
                  required
                />
              </label>
              <p className="helper">
                Почта: {user?.email}. Изменение почты и восстановление второго
                фактора — через оператора клиники.
              </p>
            </>
          ) : (
            <>
              <label>
                Текущий пароль
                <input
                  name="current_password"
                  type="password"
                  autoComplete="current-password"
                  required
                />
              </label>
              <label>
                Новый пароль
                <input
                  name="new_password"
                  type="password"
                  autoComplete="new-password"
                  minLength={12}
                  maxLength={72}
                  required
                />
              </label>
              {user?.role === "doctor" ? (
                <label>
                  Код аутентификатора
                  <input
                    name="code"
                    inputMode="numeric"
                    pattern="[0-9]{6}"
                    required
                  />
                </label>
              ) : null}
              <p className="helper">
                После смены пароля все сессии завершатся. Войдите с новым
                паролем.
              </p>
            </>
          )}
          <button className="primary" disabled={busy}>
            {busy ? "Сохраняем…" : "Сохранить изменения"}
          </button>
        </form>
      )}
    </>
  );
}

export function PasswordRecovery({
  portal,
  token,
  onBack,
}: {
  portal: "doctor" | "patient";
  token?: string | null;
  onBack: () => void;
}) {
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      if (token) {
        await endpoint("/auth/password/reset/confirm", "post", {
          body: { token, new_password: String(f.get("new_password")) },
        });
        history.replaceState(history.state, "", location.pathname);
        setNotice("Пароль изменён. Войдите с новым паролем.");
      } else {
        await api("/auth/password/reset/request", { email: f.get("email") });
        setNotice(
          "Запрос принят. Если адрес зарегистрирован, ссылка восстановления поставлена в очередь отправки. Проверьте почту.",
        );
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="recovery-page">
      <h1>{token ? "Новый пароль" : "Восстановить доступ"}</h1>
      <p>
        {portal === "doctor"
          ? "Сброс пароля не отключает второй фактор врача."
          : "Для восстановления используется почта, указанная врачом в приглашении."}
      </p>
      <form onSubmit={submit}>
        {token ? (
          <label>
            Новый пароль
            <input
              name="new_password"
              type="password"
              autoComplete="new-password"
              minLength={12}
              required
            />
          </label>
        ) : (
          <label>
            Электронная почта
            <input name="email" type="email" autoComplete="email" required />
          </label>
        )}
        <ErrorBox message={error} />
        {notice ? <Notice>{notice}</Notice> : null}
        <button className="primary" disabled={busy}>
          {busy ? "Подождите…" : token ? "Изменить пароль" : "Отправить ссылку"}
        </button>
      </form>
      <button className="text-button" onClick={onBack}>
        Вернуться ко входу
      </button>
    </main>
  );
}
