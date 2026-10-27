import { test, expect, request, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { createHmac, randomUUID } from "node:crypto";
import { LoginPage } from "./pages";
import type {
  SupportHistory,
  SupportMessage,
  SupportProvider,
} from "../packages/ui/support-chat";
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
  const bits = Array.from(secret, (ch) =>
    alphabet.indexOf(ch).toString(2).padStart(5, "0"),
  ).join("");
  const key = Buffer.from(
    (bits.match(/.{8}/g) ?? []).map((byte) => parseInt(byte, 2)),
  );
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = createHmac("sha1", key).update(counter).digest();
  return String(
    (digest.readUInt32BE(digest[digest.length - 1] & 15) & 0x7fffffff) %
      1000000,
  ).padStart(6, "0");
}
const provider: SupportProvider = {
  mode: "demo",
  model: "scripted-v1",
  disclaimer: "Тестовый помощник",
  ready: true,
  external: false,
  agent: {
    version: "support-agent-v1",
    enabled: true,
    max_model_rounds: 3,
    max_tool_calls: 4,
  },
};
const patient = {
  id: "20000000-0000-4000-8000-000000000001",
  name: "Тестовый Пациент",
  email: "patient@local.test",
  role: "patient",
  timezone: "Europe/Moscow",
};
const today = new Date().toLocaleDateString("en-CA", {
  timeZone: patient.timezone,
});
function message(
  id: string,
  text: string,
  created: string,
  role: "user" | "assistant" = "assistant",
): SupportMessage {
  return {
    id,
    text,
    role,
    created_at: created,
    provider: "demo",
    model: "scripted-v1",
    safety: "standard",
  };
}
async function fixture(page: Page) {
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { user: patient, csrf: "test-csrf" } }),
  );
  await page.route("**/api/v1/events", (route) => route.abort());
  await page.route("**/api/v1/notifications?*", (route) =>
    route.fulfill({
      json: { items: [], total: 0, unread_count: 0, next_cursor: null },
    }),
  );
  await page.route("**/api/v1/dashboard", (route) =>
    route.fulfill({
      json: {
        patient,
        entries: [],
        alerts: [],
        reviews: [],
        assignments: [
          {
            id: "active-today",
            kind: "ema",
            start_date: today,
            end_date: today,
            interval_days: 1,
            status: "active",
            due_time: "19:30:00",
          },
          {
            id: "paused",
            kind: "csd",
            start_date: today,
            end_date: today,
            interval_days: 1,
            status: "paused",
            due_time: "08:00:00",
          },
        ],
        care_team: [
          {
            id: "10000000-0000-4000-8000-000000000001",
            name: "Врач",
            care_link_id: "30000000-0000-4000-8000-000000000001",
          },
        ],
        summary: {
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
        },
        sleep_summary: {
          count: 0,
          complete: false,
          from: today,
          to: today,
          samples: {},
          total_alcohol: 0,
          total_caffeine: 0,
        },
        today,
        review_snapshot: "a".repeat(64),
      },
    }),
  );
}

test("patient support persists demo replies separately from doctor chat, protects history and replays without duplicates", async ({
  page,
}) => {
  const doctor = await request.newContext({
    baseURL,
    extraHTTPHeaders: { Origin: baseURL },
    ignoreHTTPSErrors: process.env.E2E_LOCAL_CA === "1",
  });
  try {
    const login = await doctor.post("/api/v1/auth/login", {
      data: {
        email: "doctor@psitech.local",
        password,
        code: code(),
        portal: "doctor",
      },
    });
    expect(login.ok()).toBeTruthy();
    const session = await login.json();
    const email = `support-${randomUUID()}@psitech.local`;
    const invitation = await doctor.post("/api/v1/doctor/invitations", {
      headers: { "X-CSRF-Token": session.csrf },
      data: { email, name: "Демо поддержки" },
    });
    expect(invitation.status()).toBe(201);
    expect(
      (
        await doctor.post("/api/v1/auth/accept-invitation", {
          data: {
            token: (await invitation.json()).token,
            password,
            consent: true,
          },
        })
      ).status(),
    ).toBe(201);
    const patientLogin = new LoginPage(page, "patient");
    await patientLogin.open();
    await patientLogin.login(email, password);
    await page.getByRole("button", { name: "Поддержка", exact: true }).click();
    const chat = page.getByRole("region", {
      name: "Чат поддержки",
      exact: true,
    });
    await expect(
      chat.getByText("Демонстрационный режим", { exact: true }),
    ).toBeVisible();
    await chat
      .getByRole("button", {
        name: "Объясни, как заполнять дневник самочувствия EMA.",
        exact: true,
      })
      .click();
    await chat
      .getByRole("button", { name: "Отправить помощнику", exact: true })
      .click();
    await expect(
      chat.getByText("Демо: выбор действий", { exact: true }),
    ).toBeVisible();
    await expect(
      chat.getByRole("textbox", { name: "Сообщение помощнику" }),
    ).toHaveValue("");
    await page.evaluate(() => location.reload());
    await expect(
      chat.getByText("Демо: выбор действий", { exact: true }),
    ).toBeVisible();
    const me = await (await page.request.get("/api/v1/me")).json();
    const history = (await (
      await page.request.get("/api/v1/patient/support-chat")
    ).json()) as SupportHistory;
    expect(history.items).toHaveLength(2);
    const key = randomUUID();
    const body = {
      text: "Как изменить дозу лекарства?",
      request_key: key,
      external_consent: false,
    };
    const headers = { Origin: baseURL, "X-CSRF-Token": String(me.csrf) };
    const first = await page.request.post("/api/v1/patient/support-chat", {
      headers,
      data: body,
    });
    expect(first.status()).toBe(201);
    const firstTurn = await first.json();
    expect(
      firstTurn.items.find((item: SupportMessage) => item.role === "assistant")
        .safety,
    ).toBe("medical_boundary");
    const second = await page.request.post("/api/v1/patient/support-chat", {
      headers,
      data: body,
    });
    expect(second.status()).toBe(200);
    expect(
      (await second.json()).items.map((item: SupportMessage) => item.id),
    ).toEqual(firstTurn.items.map((item: SupportMessage) => item.id));
    expect(
      (
        await page.request.post("/api/v1/patient/support-chat", {
          headers,
          data: { ...body, text: "Другой текст" },
        })
      ).status(),
    ).toBe(409);
    const crisis = await page.request.post("/api/v1/patient/support-chat", {
      headers,
      data: {
        text: "Я хочу покончить с собой",
        request_key: randomUUID(),
        external_consent: false,
      },
    });
    expect(crisis.status()).toBe(201);
    expect(
      (await crisis.json()).items.find(
        (item: SupportMessage) => item.role === "assistant",
      ).safety,
    ).toBe("crisis");
    expect((await doctor.get("/api/v1/patient/support-chat")).status()).toBe(
      403,
    );
    expect(
      (
        await doctor.post("/api/v1/patient/support-chat", {
          headers: { "X-CSRF-Token": session.csrf },
          data: body,
        })
      ).status(),
    ).toBe(403);
    const clinicalAlerts = await page.request.get(
      `/api/v1/patients/${String(me.user.id)}/alerts?limit=25`,
    );
    expect((await clinicalAlerts.json()).items).toHaveLength(0);
    const humanChat = await page.request.get(
      `/api/v1/patients/${String(me.user.id)}/messages?page=true&doctor_id=${String(session.user.id)}`,
    );
    expect((await humanChat.json()).items).toHaveLength(0);
    await page.evaluate(() => location.reload());
    await expect(
      chat.getByText("Поддержка и обращение к человеку", { exact: true }),
    ).toHaveCount(2);
    await page.screenshot({
      path: "test-results/patient-support.png",
      fullPage: true,
    });
  } finally {
    await doctor.dispose();
  }
});

test("external chat needs consent, retries the same draft key and clears private history after access denial", async ({
  page,
}) => {
  await fixture(page);
  const external = {
    ...provider,
    mode: "openai" as const,
    external: true,
    model: "test-model",
  };
  let posts = 0;
  const keys: string[] = [];
  let denied = false;
  await page.route("**/api/v1/patient/support-chat**", async (route) => {
    if (route.request().method() === "GET") {
      return route.fulfill({
        json: {
          items: [
            message(
              "private",
              "Приватная история поддержки",
              "2026-10-26T12:00:00Z",
            ),
          ],
          total: 1,
          next_cursor: null,
          provider: external,
        },
      });
    }
    posts++;
    const body = route.request().postDataJSON() as {
      request_key: string;
      text: string;
      external_consent: boolean;
    };
    keys.push(body.request_key);
    expect(body.external_consent).toBe(true);
    if (posts === 1)
      return route.fulfill({
        status: 503,
        json: { error: "Тест: временный отказ" },
      });
    denied = true;
    return route.fulfill({
      status: 403,
      json: { error: "Тест: доступ отозван" },
    });
  });
  await page.goto("/patient/?page=support");
  await expect(
    page.getByText("Приватная история поддержки", { exact: true }),
  ).toBeVisible();
  const draft = page.getByRole("textbox", { name: "Сообщение помощнику" });
  await draft.fill("Хочу поговорить о сегодняшнем дне");
  const send = page.getByRole("button", {
    name: "Отправить помощнику",
    exact: true,
  });
  await expect(send).toBeDisabled();
  expect(posts).toBe(0);
  await page
    .getByRole("checkbox", { name: /Разрешаю передать мой текст/ })
    .check();
  await send.click();
  await expect(
    page.getByText("Тест: временный отказ", { exact: true }),
  ).toBeVisible();
  await expect(draft).toHaveValue("Хочу поговорить о сегодняшнем дне");
  await send.click();
  await expect(
    page.getByText("Тест: доступ отозван", { exact: true }),
  ).toBeVisible();
  expect(denied).toBe(true);
  expect(keys).toHaveLength(2);
  expect(keys[1]).toBe(keys[0]);
  await expect(
    page.getByText("Приватная история поддержки", { exact: true }),
  ).toHaveCount(0);
  await expect(draft).toHaveValue("");
  await expect(send).toBeDisabled();
});

test("support history is chronological across pages, shows only today's active reminders and guards an unsent draft", async ({
  page,
}) => {
  await fixture(page);
  await page.route("**/api/v1/patient/support-chat**", (route) => {
    const older = new URL(route.request().url()).searchParams.has("cursor");
    return route.fulfill({
      json: {
        items: older
          ? [
              message("two", "Сообщение 2", "2026-10-26T10:02:00Z"),
              message("one", "Сообщение 1", "2026-10-26T10:01:00Z"),
            ]
          : [
              message("four", "Сообщение 4", "2026-10-26T10:04:00Z"),
              message("three", "Сообщение 3", "2026-10-26T10:03:00Z"),
            ],
        total: 4,
        next_cursor: older ? null : "previous-page",
        provider,
      },
    });
  });
  await page.goto("/patient/?page=support");
  await page
    .getByRole("button", { name: "Показать предыдущие сообщения" })
    .click();
  await expect(
    page.getByRole("log", { name: "Сообщения поддержки" }).locator("article p"),
  ).toHaveText(["Сообщение 1", "Сообщение 2", "Сообщение 3", "Сообщение 4"]);
  await expect(
    page.getByText("Дневник самочувствия EMA", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Дневник сна CSD", { exact: true })).toHaveCount(
    0,
  );
  await expect(
    page.getByText("19:30 · Ожидает заполнения", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "Сообщение помощнику" })
    .fill("Несохранённый текст");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Написать врачу", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Поддержка", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Сообщение помощнику" }),
  ).toHaveValue("Несохранённый текст");
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Открыть дневники", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: /Здравствуйте/ }),
  ).toBeVisible();
});

test("demo agent uses real task tools, remembers the conversation after reload and opens a diary without submitting it", async ({
  page,
}) => {
  const doctor = await request.newContext({
    baseURL,
    extraHTTPHeaders: { Origin: baseURL },
    ignoreHTTPSErrors: process.env.E2E_LOCAL_CA === "1",
  });
  try {
    const login = await doctor.post("/api/v1/auth/login", {
      data: {
        email: "doctor@psitech.local",
        password,
        code: code(),
        portal: "doctor",
      },
    });
    expect(login.ok()).toBeTruthy();
    const doctorSession = await login.json();
    const email = `agent-${randomUUID()}@psitech.local`;
    const invitation = await doctor.post("/api/v1/doctor/invitations", {
      headers: { "X-CSRF-Token": doctorSession.csrf },
      data: { email, name: "Демо агента" },
    });
    expect(invitation.status()).toBe(201);
    expect(
      (
        await doctor.post("/api/v1/auth/accept-invitation", {
          data: {
            token: (await invitation.json()).token,
            password,
            consent: true,
          },
        })
      ).status(),
    ).toBe(201);
    const patientLogin = new LoginPage(page, "patient");
    await patientLogin.open();
    await patientLogin.login(email, password);
    const me = await (await page.request.get("/api/v1/me")).json();
    const patientID = String(me.user.id);
    const dashboard = await (
      await page.request.get("/api/v1/dashboard")
    ).json();
    const assigned = await doctor.post(
      `/api/v1/patients/${patientID}/schedules`,
      {
        headers: { "X-CSRF-Token": doctorSession.csrf },
        data: {
          kind: "ema",
          start_date: dashboard.today,
          end_date: dashboard.today,
          due_time: "19:45",
          interval_days: 1,
          request_key: randomUUID(),
        },
      },
    );
    expect(assigned.status()).toBe(201);
    await page.goto("/patient/?page=support");
    const chat = page.getByRole("region", {
      name: "Чат поддержки",
      exact: true,
    });
    const memory = chat.getByRole("region", {
      name: "Тема и следующий шаг",
      exact: true,
    });
    const composer = chat.getByRole("textbox", {
      name: "Сообщение помощнику",
      exact: true,
    });
    const send = chat.getByRole("button", {
      name: "Отправить помощнику",
      exact: true,
    });
    await composer.fill("Какие задачи сегодня?");
    const firstRequest = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/patient/support-chat") &&
        response.request().method() === "POST",
    );
    await send.click();
    const first = (await (await firstRequest).json()).items.find(
      (item: SupportMessage) => item.role === "assistant",
    ) as SupportMessage;
    expect(
      first.agent?.tools.some(
        (tool) =>
          tool.name === "get_today_tasks" && tool.status === "completed",
      ),
    ).toBe(true);
    expect(first.agent?.state.focus).toBe("tasks");
    await expect(
      memory.getByText("Сейчас обсуждаем: Дневники на сегодня", {
        exact: true,
      }),
    ).toBeVisible();
    await chat.getByText("На что опирается ответ", { exact: true }).click();
    await expect(
      chat.getByText("Ваши задачи и заполнение дневников на сегодня", {
        exact: true,
      }),
    ).toBeVisible();
    await page.evaluate(() => location.reload());
    await expect(
      memory.getByText("Сейчас обсуждаем: Дневники на сегодня", {
        exact: true,
      }),
    ).toBeVisible();
    await composer.fill("помоги с этим");
    const secondRequest = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/patient/support-chat") &&
        response.request().method() === "POST",
    );
    await send.click();
    const second = (await (await secondRequest).json()).items.find(
      (item: SupportMessage) => item.role === "assistant",
    ) as SupportMessage;
    expect(second.agent?.previous_state_version).toBe(
      first.agent?.state.version,
    );
    expect(second.agent?.state.version).toBeGreaterThan(
      first.agent!.state.version,
    );
    expect(
      second.agent?.tools.some((tool) => tool.status === "completed"),
    ).toBe(true);
    await memory
      .getByRole("button", {
        name: "Посмотреть задачи на сегодня",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("heading", { name: /Здравствуйте/ }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Поддержка", exact: true }).click();
    await composer.fill("Объясни, как заполнять дневник самочувствия EMA.");
    const diaryRequest = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/patient/support-chat") &&
        response.request().method() === "POST",
    );
    await send.click();
    const explanation = (await (await diaryRequest).json()).items.find(
      (item: SupportMessage) => item.role === "assistant",
    ) as SupportMessage;
    expect(
      explanation.agent?.tools.some(
        (tool) => tool.name === "explain_diary" && tool.status === "completed",
      ),
    ).toBe(true);
    await memory
      .getByRole("button", {
        name: "Открыть дневник самочувствия",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("dialog", { name: "Дневник самочувствия", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Закрыть", exact: true }).click();
    const diaries = await page.request.get(
      `/api/v1/patients/${patientID}/diaries?limit=25`,
    );
    expect((await diaries.json()).items).toHaveLength(0);
    const messages = await page.request.get(
      `/api/v1/patients/${patientID}/messages?page=true&doctor_id=${String(doctorSession.user.id)}`,
    );
    expect((await messages.json()).items).toHaveLength(0);
    await page.screenshot({
      path: "test-results/patient-support-agent.png",
      fullPage: true,
    });
  } finally {
    await doctor.dispose();
  }
});

test("agent evidence stays readable alongside legacy messages and its navigation never executes model URLs or sends a message", async ({
  page,
}) => {
  await fixture(page);
  let denied = false;
  let writes = 0;
  const legacy = message(
    "legacy",
    "Старая запись без данных агента",
    "2026-10-26T10:00:00Z",
  );
  const agentMessage: SupportMessage = {
    ...message(
      "agent",
      "Можно подготовить вопрос врачу.",
      "2026-10-26T10:01:00Z",
    ),
    agent: {
      version: "support-agent-v1",
      mode: "demo",
      status: "completed",
      previous_state_version: 0,
      state: { version: 1, focus: "doctor", next_step: "doctor_message" },
      tools: [
        {
          name: "propose_support_step",
          status: "completed",
          summary: "Подготовлен небольшой шаг: обсудить вопрос с врачом.",
          result: {
            url: "https://untrusted.invalid/never-open",
            hidden: "RAW_TOOL_RESULT_MUST_NOT_RENDER",
          },
        },
      ],
      model_rounds: 0,
      tool_calls: 1,
      max_model_rounds: 3,
      max_tool_calls: 4,
    },
  };
  await page.route("**/api/v1/patient/support-chat**", (route) => {
    if (route.request().method() === "POST") {
      writes++;
      denied = true;
      return route.fulfill({
        status: 403,
        json: { error: "Тест: память недоступна" },
      });
    }
    return route.fulfill({
      json: {
        items: [agentMessage, legacy],
        total: 2,
        next_cursor: null,
        provider: {
          ...provider,
          agent: {
            enabled: true,
            version: "support-agent-v1",
            max_model_rounds: 3,
            max_tool_calls: 4,
          },
        },
      },
    });
  });
  await page.route("**/api/v1/patients/*/messages?*", (route) =>
    route.fulfill({
      json: { items: [], total: 0, next_cursor: null, unread_count: 0 },
    }),
  );
  await page.goto("/patient/?page=support");
  await expect(
    page.getByText("Старая запись без данных агента", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Сейчас обсуждаем: Вопрос врачу", { exact: true }),
  ).toBeVisible();
  await page.getByText("На что опирается ответ", { exact: true }).click();
  await expect(
    page.getByText("Подготовлен небольшой шаг: обсудить вопрос с врачом.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByText("RAW_TOOL_RESULT_MUST_NOT_RENDER", { exact: true }),
  ).toHaveCount(0);
  await expect(page.getByRole("link", { name: /untrusted/ })).toHaveCount(0);
  await page
    .getByRole("button", { name: "Перейти к переписке с врачом", exact: true })
    .click();
  await expect(
    page.getByRole("heading", {
      name: "Переписка с врачом",
      exact: true,
      level: 1,
    }),
  ).toBeVisible();
  expect(writes).toBe(0);
  agentMessage.agent!.state = {
    version: 2,
    focus: "ema",
    next_step: "open_diary",
  };
  await page.getByRole("button", { name: "Поддержка", exact: true }).click();
  await page
    .getByRole("button", { name: "Открыть дневник самочувствия", exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "Дневник самочувствия", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Закрыть", exact: true }).click();
  expect(writes).toBe(0);
  await page
    .getByRole("textbox", { name: "Сообщение помощнику", exact: true })
    .fill("Продолжим");
  await page
    .getByRole("button", { name: "Отправить помощнику", exact: true })
    .click();
  await expect(
    page.getByText("Тест: память недоступна", { exact: true }),
  ).toBeVisible();
  expect(denied).toBe(true);
  await expect(
    page.getByRole("region", { name: "Тема и следующий шаг", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText("Старая запись без данных агента", { exact: true }),
  ).toHaveCount(0);
});
