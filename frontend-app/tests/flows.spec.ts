import {
  test,
  expect,
  request,
  type APIRequestContext,
} from "@playwright/test";
import { readFileSync } from "node:fs";
import { createHmac, randomUUID } from "node:crypto";
import { LoginPage } from "./pages";
import type { Model } from "../packages/api-client";
const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:8080";
const password = readFileSync(
  new URL("../../.local/seed_password", import.meta.url),
  "utf8",
).trim();
function code() {
  const secret = readFileSync(
    new URL("../../.local/doctor_totp", import.meta.url),
    "utf8",
  ).trim();
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const ch of secret)
    bits += alphabet.indexOf(ch).toString(2).padStart(5, "0");
  const key = Buffer.from(
    (bits.match(/.{8}/g) ?? []).map((b) => parseInt(b, 2)),
  );
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const h = createHmac("sha1", key).update(counter).digest();
  const at = h[h.length - 1] & 15;
  return String((h.readUInt32BE(at) & 0x7fffffff) % 1000000).padStart(6, "0");
}
async function doctorAPI() {
  const ctx = await request.newContext({
    baseURL,
    extraHTTPHeaders: { Origin: baseURL },
  });
  const response = await ctx.post("/api/v1/auth/login", {
    data: {
      email: "doctor@psitech.local",
      password,
      code: code(),
      portal: "doctor",
    },
  });
  expect(response.ok()).toBeTruthy();
  const session = await response.json();
  return { ctx, csrf: String(session.csrf) };
}
async function createPatient(ctx: APIRequestContext, csrf: string) {
  const email = `browser-${randomUUID()}@psitech.local`;
  const name = `Тест ${randomUUID().slice(0, 8)}`;
  const response = await ctx.post("/api/v1/doctor/invitations", {
    headers: { "X-CSRF-Token": csrf },
    data: { email, name },
  });
  expect(response.status()).toBe(201);
  const invite = await response.json();
  return { email, name, token: String(invite.token) };
}

test("doctor dashboard and patient detail are separate from patient UI", async ({
  page,
}) => {
  const login = new LoginPage(page, "doctor");
  await login.open();
  await login.login("doctor@psitech.local", password, code());
  await expect(
    page.getByRole("heading", { name: "Обзор наблюдения" }),
  ).toBeVisible();
  await page.getByLabel("Поиск пациента").fill("Анна Миронова");
  await page
    .getByRole("button", { name: "Открыть профиль Анна Миронова" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Анна Миронова" }),
  ).toBeVisible();
  await expect(
    page.getByRole("img", {
      name: "Динамика настроения, энергии и тревоги по заполненным дневникам",
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Сводка сна за 14 дней" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "EMA за три дня" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Сохранить осмотр" }).click();
  await expect(
    page.getByText("Осмотр профиля сохранён", { exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: "test-results/doctor-overview.png",
    fullPage: true,
  });
  await page.getByRole("tab", { name: "Дневники", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "История дневников" }),
  ).toBeVisible();
  await page.screenshot({
    path: "test-results/doctor-profile.png",
    fullPage: true,
  });
  await page.goto("/patient/");
  await expect(
    page.getByRole("heading", { name: "Это кабинет пациента" }),
  ).toBeVisible();
});

test("invited patient submits sleep, risk signal, EMA and correction; doctor acknowledges", async ({
  page,
  browser,
}) => {
  const { ctx, csrf } = await doctorAPI();
  const invited = await createPatient(ctx, csrf);
  await page.goto(`/patient/#invite=${invited.token}`);
  await page.getByLabel("Пароль", { exact: true }).fill(password);
  await page.getByRole("checkbox").check();
  await page.getByRole("button", { name: "Создать аккаунт" }).click();
  await expect(
    page.getByText("Регистрация завершена.", { exact: false }),
  ).toBeVisible();
  await new LoginPage(page, "patient").login(invited.email, password);
  const me = await page.request.get("/api/v1/me");
  const patient = await me.json();
  const id = String(patient.user.id);
  const today = new Date().toLocaleDateString("en-CA", {
    timeZone: "Europe/Moscow",
  });
  for (const kind of ["ema", "csd"]) {
    const a = await ctx.post(`/api/v1/patients/${id}/assignments`, {
      headers: { "X-CSRF-Token": csrf },
      data: { kind, start_date: today, end_date: today, due_time: "09:00" },
    });
    expect(a.ok()).toBeTruthy();
  }
  await page.reload();
  await page
    .getByRole("article")
    .filter({ has: page.getByRole("heading", { name: "Как вы спали?" }) })
    .getByRole("button", { name: "Заполнить" })
    .click();
  await page.getByLabel("Во сколько легли в постель?").fill("23:00");
  await page.getByLabel("Во сколько встали с постели?").fill("07:00");
  await page.getByLabel("Общее время сна, мин").fill("420");
  await page.getByRole("button", { name: "Сохранить дневник" }).click();
  await expect(
    page.getByText("Дневник сна заполнен", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("article")
    .filter({
      has: page.getByRole("heading", { name: "Как прошёл ваш день?" }),
    })
    .getByRole("button", { name: "Заполнить" })
    .click();
  await page.getByLabel("Настроение", { exact: false }).selectOption("4");
  await page
    .getByLabel("Интерес к привычным делам", { exact: false })
    .selectOption("3");
  await page.getByLabel("Энергия", { exact: false }).selectOption("7");
  await page.getByLabel("Тревога", { exact: false }).selectOption("2");
  await page
    .getByLabel("Были ли сегодня суицидальные мысли?")
    .selectOption("yes");
  await expect(
    page.getByText("Сигнал сохранён и доступен врачу", { exact: false }),
  ).toBeVisible();
  // Verify the alert exists before the full form is submitted.
  let dashboard = await ctx.get("/api/v1/dashboard");
  let dashboardData = await dashboard.json();
  expect(
    dashboardData.alerts.some(
      (a: { patient_id: string }) => a.patient_id === id,
    ),
  ).toBeTruthy();
  await page.getByRole("button", { name: "Сохранить дневник" }).click();
  await expect(
    page.getByText("Дневник самочувствия заполнен", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Дневники", exact: true }).click();
  await page
    .getByRole("button", { name: `Исправить самочувствие ${today}` })
    .click();
  await page.getByLabel("Настроение", { exact: false }).selectOption("2");
  await page
    .getByLabel("Были ли сегодня суицидальные мысли?")
    .selectOption("no");
  await page.getByRole("button", { name: "Сохранить исправление" }).click();
  await expect(
    page.getByText("Настроение 2 · Энергия 7 · Тревога 2", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Предыдущие версии" }).click();
  await expect(
    page
      .getByRole("dialog", { name: "Версии записи" })
      .getByText("Настроение 4 · Энергия 7 · Тревога 2", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("dialog", { name: "Версии записи" })
    .getByRole("button", { name: "Закрыть", exact: true })
    .click();
  await page.getByRole("button", { name: "Переписка", exact: true }).click();
  await page
    .getByLabel("Сообщение", { exact: true })
    .fill("Сообщение из браузерного теста");
  await page.getByRole("button", { name: "Отправить", exact: true }).click();
  await expect(
    page.getByText("Сообщение из браузерного теста", { exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Сегодня", exact: true }).click();
  await page.screenshot({
    path: "test-results/patient-mobile.png",
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  const dc = await browser.newContext();
  const doctorPage = await dc.newPage();
  const doctorLogin = new LoginPage(doctorPage, "doctor");
  await doctorLogin.open();
  await doctorLogin.login("doctor@psitech.local", password, code());
  await doctorPage
    .getByRole("button", { name: "Сигналы", exact: false })
    .click();
  const alert = doctorPage
    .getByRole("article")
    .filter({ hasText: invited.name });
  await alert.getByRole("button", { name: "Ознакомлен", exact: true }).click();
  await expect(alert.getByText("Ознакомлен", { exact: true })).toBeVisible();
  dashboard = await ctx.get("/api/v1/dashboard");
  dashboardData = await dashboard.json();
  expect(
    dashboardData.alerts.find(
      (a: { patient_id: string }) => a.patient_id === id,
    ).status,
  ).toBe("ack");
  await dc.close();
  await ctx.dispose();
});

test("patient cannot call doctor routes or another patient profile", async ({
  page,
}) => {
  const login = new LoginPage(page, "patient");
  await login.open();
  await login.login("patient@psitech.local", password);
  const response = await page.request.get("/api/v1/doctor/patients");
  expect(response.status()).toBe(403);
  const denied = await page.request.get(
    "/api/v1/patients/00000000-0000-4000-8000-000000000099",
  );
  expect(denied.status()).toBe(403);
  await page.getByRole("button", { name: "Выйти", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Войти в кабинет" }),
  ).toBeVisible();
});

test("invitation registration and both-role in-app notifications work without an email provider", async ({
  page,
}) => {
  const { ctx, csrf } = await doctorAPI();
  try {
    const invited = await createPatient(ctx, csrf);
    await page.goto(`/patient/#invite=${invited.token}`);
    await page.getByLabel("Пароль", { exact: true }).fill(password);
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Создать аккаунт" }).click();
    await expect(
      page.getByText("Регистрация завершена.", { exact: false }),
    ).toBeVisible();
    await new LoginPage(page, "patient").login(invited.email, password);
    const session = await (await page.request.get("/api/v1/me")).json();
    const patientID = String(session.user.id);
    const doctorID = String(
      (await (await ctx.get("/api/v1/me")).json()).user.id,
    );
    const body = `Сообщение после регистрации ${randomUUID()}`;
    const sent = await page.request.post(
      `/api/v1/patients/${patientID}/messages`,
      {
        headers: { Origin: baseURL, "X-CSRF-Token": String(session.csrf) },
        data: { body, doctor_id: doctorID, request_key: randomUUID() },
      },
    );
    expect(sent.status()).toBe(201);
    type Notification = Model<"Notification">;
    async function notices(context: APIRequestContext) {
      const response = await context.get(
        "/api/v1/notifications?page=true&limit=100",
      );
      expect(response.ok()).toBeTruthy();
      return (await response.json()).items as Notification[];
    }
    await expect
      .poll(async () =>
        (await notices(ctx)).some(
          (n) => n.type === "invitation.accepted" && n.patient_id === patientID,
        ),
      )
      .toBeTruthy();
    await expect
      .poll(async () =>
        (await notices(ctx)).some(
          (n) => n.type === "message.sent" && n.patient_id === patientID,
        ),
      )
      .toBeTruthy();
    const doctorNotices = await notices(ctx);
    const incoming = doctorNotices.find(
      (n) => n.type === "message.sent" && n.patient_id === patientID,
    );
    expect(incoming?.target_type).toBe("conversation");
    expect(incoming?.is_read).toBe(false);
    const reply = `Ответ врача ${randomUUID()}`;
    expect(
      (
        await ctx.post(`/api/v1/patients/${patientID}/messages`, {
          headers: { "X-CSRF-Token": csrf },
          data: { body: reply, request_key: randomUUID() },
        })
      ).status(),
    ).toBe(201);
    await expect
      .poll(async () =>
        (await notices(page.request)).some(
          (n) => n.type === "message.sent" && n.patient_id === patientID,
        ),
      )
      .toBeTruthy();
    await page.reload();
    await page
      .getByRole("button", { name: "Уведомления", exact: true })
      .click();
    const patientNotices = await notices(page.request);
    const replyNotice = patientNotices.find(
      (n) => n.type === "message.sent" && n.patient_id === patientID,
    );
    expect(replyNotice?.target_type).toBe("conversation");
    expect(replyNotice?.doctor_id).toBe(doctorID);
    await page
      .getByTestId(`notification-${replyNotice!.id}`)
      .getByRole("button", { name: "Открыть", exact: true })
      .click();
    await expect(page).toHaveURL(/page=messages/);
    await expect(page.getByText(reply, { exact: true })).toBeVisible();
    await expect
      .poll(
        async () =>
          (await notices(page.request)).find((n) => n.id === replyNotice!.id)
            ?.is_read,
      )
      .toBe(true);
    // Reading the patient's notification must not clear the doctor's unread message.
    expect(
      (await notices(ctx)).find((n) => n.id === incoming!.id)?.is_read,
    ).toBe(false);
  } finally {
    await ctx.dispose();
  }
});

test("stalled early risk request times out and full diary save remains available", async ({
  page,
}) => {
  const { ctx, csrf } = await doctorAPI();
  const invited = await createPatient(ctx, csrf);
  const accepted = await ctx.post("/api/v1/auth/accept-invitation", {
    data: { token: invited.token, password, consent: true },
  });
  expect(accepted.status()).toBe(201);
  const login = new LoginPage(page, "patient");
  await login.open();
  await login.login(invited.email, password);
  const session = await (await page.request.get("/api/v1/me")).json();
  const today = new Date().toLocaleDateString("en-CA", {
    timeZone: "Europe/Moscow",
  });
  const assignment = await ctx.post(
    `/api/v1/patients/${session.user.id}/assignments`,
    {
      headers: { "X-CSRF-Token": csrf },
      data: {
        kind: "ema",
        start_date: today,
        end_date: today,
        due_time: "20:00",
      },
    },
  );
  expect(assignment.ok()).toBeTruthy();
  await page.reload();
  await page
    .getByRole("article")
    .filter({
      has: page.getByRole("heading", { name: "Как прошёл ваш день?" }),
    })
    .getByRole("button", { name: "Заполнить" })
    .click();
  await page.getByLabel("Настроение", { exact: false }).selectOption("4");
  await page
    .getByLabel("Интерес к привычным делам", { exact: false })
    .selectOption("3");
  await page.getByLabel("Энергия", { exact: false }).selectOption("7");
  await page.getByLabel("Тревога", { exact: false }).selectOption("2");
  await page.route("**/api/v1/patient/risk-signals", () => {});
  await page
    .getByLabel("Были ли сегодня суицидальные мысли?")
    .selectOption("yes");
  await expect(
    page.getByRole("button", { name: "Повторить отправку сигнала" }),
  ).toBeVisible({ timeout: 18000 });
  await expect(
    page.getByRole("button", { name: "Сохранить дневник" }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Сохранить дневник" }).click();
  await expect(
    page.getByText("Дневник самочувствия заполнен", { exact: true }),
  ).toBeVisible();
  const alerts = await (await ctx.get("/api/v1/dashboard")).json();
  expect(
    alerts.alerts.some(
      (a: { patient_id: string }) => a.patient_id === session.user.id,
    ),
  ).toBeTruthy();
  await ctx.dispose();
});
