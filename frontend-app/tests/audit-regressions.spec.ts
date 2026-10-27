import { test, expect, type Page } from "@playwright/test";
const doctor = {
  id: "10000000-0000-4000-8000-000000000001",
  name: "Тестовый Врач",
  email: "doctor@local.test",
  role: "doctor",
  timezone: "Europe/Moscow",
};
const patient = {
  ...doctor,
  id: "20000000-0000-4000-8000-000000000001",
  role: "patient",
  name: "Тестовый Пациент",
  email: "patient@local.test",
};
const today = new Date().toLocaleDateString("en-CA", {
  timeZone: "Europe/Moscow",
});
const summary = {
  count: 0,
  complete: false,
  mood: null,
  anhedonia: null,
  energy: null,
  anxiety: null,
  suicidal_days: 0,
  mood_delta: null,
  max_anxiety: null,
  min_energy: null,
};
const sleep = {
  count: 0,
  complete: false,
  from: today,
  to: today,
  samples: {},
  total_alcohol: 0,
  total_caffeine: 0,
};
const detail = {
  patient,
  entries: [],
  alerts: [],
  assignments: [],
  reviews: [],
  care_team: [
    {
      id: doctor.id,
      name: doctor.name,
      care_link_id: "30000000-0000-4000-8000-000000000001",
    },
  ],
  summary,
  sleep_summary: sleep,
  today,
  review_snapshot: "a".repeat(64),
};
async function fixture(page: Page, role: "doctor" | "patient" = "doctor") {
  await page.route("**/api/v1/me", (r) =>
    r.fulfill({
      json: { user: role === "doctor" ? doctor : patient, csrf: "test-csrf" },
    }),
  );
  await page.route("**/api/v1/events", (r) => r.abort());
  await page.route("**/api/v1/notifications?*", (r) =>
    r.fulfill({
      json: { items: [], total: 0, unread_count: 0, next_cursor: null },
    }),
  );
  await page.route("**/api/v1/dashboard", (r) =>
    r.fulfill({
      json:
        role === "doctor"
          ? {
              patients: [],
              alerts: [],
              alerts_total: 0,
              patients_total: 0,
              pending_reviews_total: 0,
            }
          : detail,
    }),
  );
  await page.route(`**/api/v1/patients/${patient.id}`, (r) =>
    r.fulfill({ json: detail }),
  );
  await page.route("**/api/v1/patients/*/reviews?*", (r) =>
    r.fulfill({ json: { items: [], total: 0, next_cursor: null } }),
  );
  await page.route("**/api/v1/patients/*/alerts?*", (r) =>
    r.fulfill({ json: { items: [], total: 0, next_cursor: null } }),
  );
}

test("failed dashboard never claims there are no alerts", async ({ page }) => {
  await fixture(page);
  await page.route("**/api/v1/dashboard", (r) =>
    r.fulfill({ status: 503, json: { error: "Тест: данные недоступны" } }),
  );
  await page.goto("/doctor/?page=alerts");
  await expect(
    page.getByText("Тест: данные недоступны", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Активных сигналов нет. Новые ответы появятся здесь.", {
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Повторить загрузку" }),
  ).toBeVisible();
});

test("global unread count, exact per-item read and notification navigation", async ({
  page,
}) => {
  await fixture(page);
  const read = new Set<string>();
  const writes: unknown[] = [];
  const notices = Array.from({ length: 25 }, (_, i) => ({
    id: `notice-${i}`,
    seq: 100 - i,
    event_id: `event-${i}`,
    type: "message.sent",
    created_at: new Date().toISOString(),
    patient_id: patient.id,
    patient_name: patient.name,
    is_read: read.has(`notice-${i}`),
    target_type: "conversation",
    target_id: "conversation-id",
    doctor_id: doctor.id,
  }));
  await page.route("**/api/v1/notifications?*", (r) =>
    r.fulfill({
      json: {
        items: notices.map((n) => ({ ...n, is_read: read.has(n.id) })),
        total: 80,
        unread_count: 68 - read.size,
        next_cursor: "older",
      },
    }),
  );
  await page.route("**/api/v1/notifications/read", async (r) => {
    const body = r.request().postDataJSON();
    writes.push(body);
    for (const id of body.ids ?? []) read.add(id);
    await r.fulfill({ json: { ok: true } });
  });
  await page.route("**/api/v1/patients/*/messages?*", (r) =>
    r.fulfill({
      json: { items: [], total: 0, unread_count: 0, next_cursor: null },
    }),
  );
  await page.goto("/doctor/");
  await page.getByRole("button", { name: "Уведомления", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Уведомления (68 новых)" }),
  ).toBeVisible();
  await page
    .getByTestId("notification-notice-0")
    .getByRole("button", { name: "Отметить прочитанным" })
    .click();
  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0]).toEqual({ ids: ["notice-0"] });
  await expect(
    page.getByRole("heading", { name: "Уведомления (67 новых)" }),
  ).toBeVisible();
  await page
    .getByTestId("notification-notice-0")
    .getByRole("button", { name: "Открыть", exact: true })
    .click();
  await expect(page).toHaveURL(new RegExp(`patient=${patient.id}`));
  await expect(
    page.getByRole("tab", { name: "Переписка", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
});

test("alerts pagination reaches an older open alert and totals are not truncated", async ({
  page,
}) => {
  await fixture(page);
  await page.route("**/api/v1/dashboard", (r) =>
    r.fulfill({
      json: {
        patients: [],
        alerts: [],
        alerts_total: 26,
        patients_total: 0,
        pending_reviews_total: 0,
      },
    }),
  );
  await page.route("**/api/v1/doctor/alerts?*", (r) => {
    const older = new URL(r.request().url()).searchParams.get("cursor");
    const items = Array.from({ length: older ? 1 : 25 }, (_, i) => ({
      id: `alert-${older ? "old" : i}`,
      patient_id: patient.id,
      patient_name: older ? "Давний открытый сигнал" : `Пациент ${i}`,
      type: "SUICIDE",
      status: "new",
      created_at: new Date().toISOString(),
      overdue: true,
    }));
    return r.fulfill({
      json: { items, total: 26, next_cursor: older ? null : "older" },
    });
  });
  await page.goto("/doctor/?page=alerts");
  await expect(page.getByText("Показано 25 из 26")).toBeVisible();
  await page.getByRole("button", { name: "Показать ещё" }).click();
  await expect(
    page.getByText("Давний открытый сигнал", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Показано 26 из 26")).toBeVisible();
});

test("draft guard prevents accidental loss and restores deep linked tab", async ({
  page,
}) => {
  await fixture(page, "patient");
  const assignment = {
    id: "assignment",
    kind: "ema",
    start_date: today,
    end_date: today,
    due_time: "20:00:00",
    status: "active",
    interval_days: 1,
  };
  await page.route("**/api/v1/dashboard", (r) =>
    r.fulfill({ json: { ...detail, assignments: [assignment] } }),
  );
  await page.goto("/patient/");
  await page.getByRole("button", { name: "Заполнить", exact: true }).click();
  await page.getByLabel("Настроение", { exact: false }).selectOption("4");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "Дневник самочувствия" }),
  ).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "Динамика", exact: true }).click();
  await expect(page).toHaveURL(/page=progress/);
  // Native reload preserves pushState in Firefox; Playwright page.reload adds an entry.
  await Promise.all([
    page.waitForEvent("load"),
    page.evaluate(() => location.reload()),
  ]);
  await expect(
    page.getByRole("heading", { name: "Как меняется самочувствие" }),
  ).toBeVisible();
  await page.goBack();
  await expect(
    page.getByRole("heading", { name: "Ваш день", exact: true }),
  ).toBeVisible();
});

test("audio upload can be cancelled without losing its draft when models are cold", async ({
  page,
}) => {
  await fixture(page, "patient");
  await page.route("**/api/v1/audio/status", (r) =>
    r.fulfill({
      json: {
        status: "not_ready",
        models: {
          asr: { state: "not_ready" },
          embedding: { state: "not_ready" },
        },
      },
    }),
  );
  await page.route("**/api/v1/patients/*/audio?*", (r) =>
    r.fulfill({ json: { recordings: [], total: 0, next_cursor: null } }),
  );
  const uploadKeys: string[] = [];
  await page.route("**/api/v1/patient/audio?*", (r) => {
    uploadKeys.push(new URL(r.request().url()).searchParams.get("key")!);
    return new Promise(() => {});
  });
  await page.goto("/patient/?page=audio");
  await expect(
    page.getByRole("heading", { name: "Обработка готова частично" }),
  ).toBeVisible();
  await page.getByLabel("Устройство и микрофон").fill("Тестовый микрофон");
  await page.getByRole("checkbox", { name: /Я согласен/ }).check();
  await page.getByLabel("Или загрузить аудиофайл").setInputFiles({
    name: "draft.wav",
    mimeType: "audio/wav",
    buffer: Buffer.alloc(1000),
  });
  await page.getByRole("button", { name: "Отправить на анализ" }).click();
  await page.getByRole("button", { name: "Отменить загрузку" }).click();
  await expect(
    page.getByText("Загрузка отменена. Черновик сохранён.", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Отправить на анализ" }),
  ).toBeEnabled();
  await expect.poll(() => uploadKeys.length).toBe(1);
  // Retrying an unchanged draft must retain the key, even after cancellation.
  await page.getByRole("button", { name: "Отправить на анализ" }).click();
  await expect.poll(() => uploadKeys.length).toBe(2);
  await page.getByRole("button", { name: "Отменить загрузку" }).click();
  expect(uploadKeys[1]).toBe(uploadKeys[0]);
  await page.getByLabel("Устройство и микрофон").fill("Другой микрофон");
  await page.getByLabel("Задание", { exact: true }).selectOption("free");
  await page.getByRole("button", { name: "Отправить на анализ" }).click();
  await expect.poll(() => uploadKeys.length).toBe(3);
  await page.getByRole("button", { name: "Отменить загрузку" }).click();
  expect(uploadKeys[2]).not.toBe(uploadKeys[1]);
});

test("disabled email recovery shows the service error without claiming a link was sent", async ({
  page,
}) => {
  await page.route("**/api/v1/me", (r) =>
    r.fulfill({ status: 401, json: { error: "Требуется вход" } }),
  );
  await page.route("**/api/v1/auth/password/reset/request", (r) =>
    r.fulfill({ status: 503, json: { error: "Отправка почты не настроена" } }),
  );
  await page.goto("/patient/");
  await page
    .getByRole("button", { name: "Не помню пароль", exact: true })
    .click();
  await page.getByLabel("Электронная почта").fill("patient@local.test");
  await page
    .getByRole("button", { name: "Отправить ссылку", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Отправка почты не настроена",
  );
  await expect(
    page.getByText(/Запрос принят|ссылка восстановления поставлена/),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Отправить ссылку", exact: true }),
  ).toBeEnabled();
});

for (const mimeType of ["audio/x-m4a", "application/octet-stream"]) {
  test(`M4A file-first upload normalizes ${mimeType} to audio/mp4`, async ({
    page,
  }) => {
    await fixture(page, "patient");
    await page.route("**/api/v1/audio/status", (r) =>
      r.fulfill({ json: { status: "not_ready", models: {} } }),
    );
    await page.route("**/api/v1/patients/*/audio?*", (r) =>
      r.fulfill({ json: { recordings: [], total: 0, next_cursor: null } }),
    );
    let receivedType = "";
    await page.route("**/api/v1/patient/audio?*", (r) => {
      receivedType = r.request().headers()["content-type"];
      return r.fulfill({
        status: 201,
        json: { id: "30000000-0000-4000-8000-000000000001" },
      });
    });
    await page.goto("/patient/?page=audio");
    await page.getByLabel("Или загрузить аудиофайл").setInputFiles({
      name: "sample.m4a",
      mimeType,
      buffer: Buffer.alloc(1000),
    });
    await page.getByLabel("Задание", { exact: true }).selectOption("free");
    await page.getByLabel("Устройство и микрофон").fill("Микрофон телефона");
    await page.getByRole("checkbox", { name: /Я согласен/ }).check();
    await expect(
      page.getByRole("button", { name: "Отправить на анализ" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "Отправить на анализ" }).click();
    await expect(
      page.getByText("Запись сохранена. Анализ выполняется в фоне.", {
        exact: true,
      }),
    ).toBeVisible();
    expect(receivedType).toBe("audio/mp4");
  });
}

for (const action of ["cancel", "timeout"]) {
  test(`pending microphone permission can ${action}, stop a late stream and recover file upload`, async ({
    page,
  }) => {
    await fixture(page, "patient");
    await page.clock.install();
    await page.route("**/api/v1/audio/status", (r) =>
      r.fulfill({ json: { status: "not_ready", models: {} } }),
    );
    await page.route("**/api/v1/patients/*/audio?*", (r) =>
      r.fulfill({ json: { recordings: [], total: 0, next_cursor: null } }),
    );
    await page.route("**/api/v1/patient/audio?*", (r) =>
      r.fulfill({
        status: 201,
        json: { id: "30000000-0000-4000-8000-000000000001" },
      }),
    );
    await page.goto("/patient/?page=audio");
    await page.evaluate(() => {
      const state = { stopped: 0, resolve: () => {} };
      Object.defineProperty(window, "micTest", { value: state });
      Object.defineProperty(navigator.mediaDevices, "getUserMedia", {
        configurable: true,
        value: () =>
          new Promise<MediaStream>((resolve) => {
            state.resolve = () =>
              resolve({
                getTracks: () => [{ stop: () => state.stopped++ }],
              } as unknown as MediaStream);
          }),
      });
    });
    await page.getByLabel("Устройство и микрофон").fill("Микрофон телефона");
    await page.getByRole("checkbox", { name: /Я согласен/ }).check();
    await page
      .getByRole("button", { name: "Начать запись", exact: true })
      .click();
    await expect(page.getByLabel("Или загрузить аудиофайл")).toBeDisabled();
    if (action === "cancel")
      await page
        .getByRole("button", {
          name: "Отменить доступ к микрофону",
          exact: true,
        })
        .click();
    else await page.clock.fastForward(15001);
    await expect(page.getByRole("alert")).toContainText(
      action === "cancel" ? "отменён" : "15 секунд",
    );
    await expect(page.getByLabel("Или загрузить аудиофайл")).toBeEnabled();
    await page.evaluate(() =>
      (
        window as typeof window & { micTest: { resolve: () => void } }
      ).micTest.resolve(),
    );
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (window as typeof window & { micTest: { stopped: number } }).micTest
              .stopped,
        ),
      )
      .toBe(1);
    await expect(
      page.getByRole("button", { name: /Остановить запись/ }),
    ).toHaveCount(0);
    await page.getByLabel("Или загрузить аудиофайл").setInputFiles({
      name: "fallback.wav",
      mimeType: "audio/wav",
      buffer: Buffer.alloc(1000),
    });
    await expect(
      page.getByRole("button", { name: "Отправить на анализ" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "Отправить на анализ" }).click();
    await expect(
      page.getByText("Запись сохранена. Анализ выполняется в фоне.", {
        exact: true,
      }),
    ).toBeVisible();
  });
}

test("clinical editor retains a failed-save draft and idempotency key, then requires explicit conflict reload", async ({
  page,
}) => {
  await fixture(page);
  const context = {
    medical_history: "",
    psychiatric_history: "",
    diagnoses: "Исходная запись",
    medications: "",
    confounders: "",
    external_scales: [],
  };
  let latest = { version: 1, context };
  const writes: { request_key: string; base_version: number }[] = [];
  await page.route("**/api/v1/patients/*/clinical-context/history?*", (r) =>
    r.fulfill({ json: { items: [], total: 0, next_cursor: null } }),
  );
  await page.route(`**/api/v1/patients/${patient.id}/clinical-context`, (r) => {
    if (r.request().method() === "GET") return r.fulfill({ json: latest });
    writes.push(r.request().postDataJSON());
    if (writes.length === 1)
      return r.fulfill({
        status: 503,
        json: { error: "Временный сбой сохранения" },
      });
    latest = {
      version: 2,
      context: { ...context, diagnoses: "Актуальная запись другого врача" },
    };
    return r.fulfill({
      status: 409,
      json: { error: "Контекст изменён другим врачом" },
    });
  });
  await page.goto(`/doctor/?patient=${patient.id}&tab=clinical`);
  await page
    .getByRole("button", {
      name: "Редактировать клинический контекст",
      exact: true,
    })
    .click();
  await page
    .getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true })
    .fill("Несохранённый черновик");
  const save = page.getByRole("button", {
    name: "Сохранить новую версию контекста",
    exact: true,
  });
  await save.click();
  await expect(page.getByRole("alert")).toContainText(
    "Временный сбой сохранения",
  );
  await expect(
    page.getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true }),
  ).toHaveValue("Несохранённый черновик");
  await save.click();
  await expect(
    page.getByRole("button", {
      name: "Загрузить актуальную версию",
      exact: true,
    }),
  ).toBeVisible();
  await expect(save).toBeDisabled();
  await expect(
    page.getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true }),
  ).toHaveValue("Несохранённый черновик");
  expect(writes).toHaveLength(2);
  expect(writes[0].request_key).toBe(writes[1].request_key);
  expect(writes[1].base_version).toBe(1);
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Загрузить актуальную версию", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true }),
  ).toHaveValue("Актуальная запись другого врача");
  await expect(save).toBeEnabled();
});

test("clinical context and paginated history are cleared when care access is revoked", async ({
  page,
}) => {
  await fixture(page);
  let denied = false;
  const privateText = "Скрыть после отзыва доступа";
  const version = {
    id: "40000000-0000-4000-8000-000000000001",
    patient_id: patient.id,
    version: 1,
    context: { diagnoses: privateText, external_scales: [] },
    author_id: doctor.id,
    author_name: doctor.name,
    created_at: new Date().toISOString(),
  };
  await page.route(`**/api/v1/patients/${patient.id}/clinical-context`, (r) =>
    denied
      ? r.fulfill({ status: 403, json: { error: "Доступ к пациенту отозван" } })
      : r.fulfill({ json: version }),
  );
  await page.route("**/api/v1/patients/*/clinical-context/history?*", (r) =>
    denied
      ? r.fulfill({ status: 403, json: { error: "Доступ к пациенту отозван" } })
      : r.fulfill({ json: { items: [version], total: 1, next_cursor: null } }),
  );
  await page.goto(`/doctor/?patient=${patient.id}&tab=clinical`);
  await expect(
    page.getByText(privateText, { exact: true }).first(),
  ).toBeVisible();
  await expect(
    page.locator("summary").filter({ hasText: "Версия 1" }),
  ).toBeVisible();
  denied = true;
  await page.evaluate(() => window.dispatchEvent(new Event("data-refresh")));
  await expect(
    page.getByText("Доступ к пациенту отозван", { exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByText(privateText, { exact: true })).toHaveCount(0);
  await expect(
    page.locator("summary").filter({ hasText: "Версия 1" }),
  ).toHaveCount(0);
});

test("clinical write denial refreshes and clears private facts and draft immediately", async ({
  page,
}) => {
  await fixture(page);
  let denied = false;
  const privateText = "Приватный анамнез до отзыва";
  const version = {
    version: 1,
    context: {
      medical_history: privateText,
      diagnoses: "Исходная запись",
      external_scales: [],
    },
  };
  await page.route("**/api/v1/patients/*/clinical-context/history?*", (r) =>
    r.fulfill({ json: { items: [], total: 0, next_cursor: null } }),
  );
  await page.route(`**/api/v1/patients/${patient.id}/clinical-context`, (r) => {
    if (r.request().method() === "POST") denied = true;
    return denied
      ? r.fulfill({ status: 403, json: { error: "Доступ к пациенту отозван" } })
      : r.fulfill({ json: version });
  });
  await page.goto(`/doctor/?patient=${patient.id}&tab=clinical`);
  await expect(page.getByText(privateText, { exact: true })).toBeVisible();
  await page
    .getByRole("button", {
      name: "Редактировать клинический контекст",
      exact: true,
    })
    .click();
  await page
    .getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true })
    .fill("Приватный несохранённый черновик");
  await page
    .getByRole("button", {
      name: "Сохранить новую версию контекста",
      exact: true,
    })
    .click();
  await expect(
    page.getByText("Доступ к пациенту отозван", { exact: true }).first(),
  ).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText(privateText, { exact: true })).toHaveCount(0);
});
