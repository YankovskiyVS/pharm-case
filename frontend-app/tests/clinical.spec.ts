import { test, expect, request } from "@playwright/test";
import { readFileSync } from "node:fs";
import { createHmac, randomUUID } from "node:crypto";
import { LoginPage } from "./pages";
import type { Model } from "../packages/api-client";
import type { ClinicalReport } from "../packages/ui/clinical-reports";
const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:8080";
const password = readFileSync(
  new URL("../../.local/seed_password", import.meta.url),
  "utf8",
).trim();
function doctorCode() {
  const secret = readFileSync(
    new URL("../../.local/doctor_totp", import.meta.url),
    "utf8",
  ).trim();
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const bits = Array.from(secret, (ch) =>
    alphabet.indexOf(ch).toString(2).padStart(5, "0"),
  ).join("");
  const key = Buffer.from(
    (bits.match(/.{8}/g) ?? []).map((b) => parseInt(b, 2)),
  );
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = createHmac("sha1", key).update(counter).digest();
  return String(
    (digest.readUInt32BE(digest[digest.length - 1] & 15) & 0x7fffffff) %
      1000000,
  ).padStart(6, "0");
}
function toneWav() {
  const rate = 16000,
    count = 6 * rate,
    wav = Buffer.alloc(44 + count * 2);
  wav.write("RIFF", 0);
  wav.writeUInt32LE(36 + count * 2, 4);
  wav.write("WAVEfmt ", 8);
  wav.writeUInt32LE(16, 16);
  wav.writeUInt16LE(1, 20);
  wav.writeUInt16LE(1, 22);
  wav.writeUInt32LE(rate, 24);
  wav.writeUInt32LE(rate * 2, 28);
  wav.writeUInt16LE(2, 32);
  wav.writeUInt16LE(16, 34);
  wav.write("data", 36);
  wav.writeUInt32LE(count * 2, 40);
  for (let i = 0; i < count; i++)
    wav.writeInt16LE(
      Math.round(0.2 * 32767 * Math.sin((2 * Math.PI * 180 * i) / rate)),
      44 + i * 2,
    );
  return wav;
}

test("doctor versions context, creates a clearly synthetic report and signs a conclusion; patient access is denied", async ({
  page,
  browser,
}) => {
  test.setTimeout(120000);
  const doctor = await request.newContext({
    baseURL,
    ignoreHTTPSErrors: process.env.E2E_LOCAL_CA === "1",
    extraHTTPHeaders: { Origin: baseURL },
  });
  const doctorBrowser = await browser.newContext({
    baseURL,
    ignoreHTTPSErrors: process.env.E2E_LOCAL_CA === "1",
  });
  try {
    const login = await doctor.post("/api/v1/auth/login", {
      data: {
        email: "doctor@psitech.local",
        password,
        code: doctorCode(),
        portal: "doctor",
      },
    });
    expect(login.ok()).toBeTruthy();
    const doctorSession = await login.json();
    const unique = randomUUID();
    const email = `clinical-${unique}@psitech.local`;
    const invitation = await doctor.post("/api/v1/doctor/invitations", {
      headers: { "X-CSRF-Token": doctorSession.csrf },
      data: { name: `Клинический тест ${unique.slice(0, 6)}`, email },
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
    const patientSession = await (await page.request.get("/api/v1/me")).json();
    const patientID = String(patientSession.user.id);
    const audioResponse = await page.request.post(
      `/api/v1/patient/audio?${new URLSearchParams({ task: "vowel", device: "Clinical E2E synthetic tone", key: randomUUID() })}`,
      {
        headers: {
          Origin: baseURL,
          "X-CSRF-Token": patientSession.csrf,
          "X-Audio-Consent": "audio-v1",
          "Content-Type": "audio/wav",
        },
        data: toneWav(),
      },
    );
    expect(audioResponse.status()).toBe(201);
    const audioID = String((await audioResponse.json()).id);
    await expect
      .poll(
        async () => {
          const result = (await (
            await page.request.get(`/api/v1/patients/${patientID}/audio`)
          ).json()) as Model<"AudioList">;
          return result.recordings.find((item) => item.id === audioID)?.status;
        },
        { timeout: 90000 },
      )
      .toBe("ready");
    const doctorPage = await doctorBrowser.newPage();
    const doctorLogin = new LoginPage(doctorPage, "doctor");
    await doctorLogin.open();
    await doctorLogin.login("doctor@psitech.local", password, doctorCode());
    await doctorPage.goto(`/doctor/?patient=${patientID}&tab=clinical`);
    await doctorPage
      .getByRole("button", {
        name: "Редактировать клинический контекст",
        exact: true,
      })
      .click();
    const privateDiagnosis = `Приватная запись врача ${unique}`;
    await doctorPage
      .getByRole("textbox", { name: "Диагнозы (запись врача)", exact: true })
      .fill(privateDiagnosis);
    await doctorPage
      .getByRole("textbox", {
        name: "Препараты и приём / клинические рекомендации",
        exact: true,
      })
      .fill("Сведения о препаратах внесены врачом из внешней документации");
    await doctorPage
      .getByRole("button", { name: "Добавить внешнюю оценку", exact: true })
      .click();
    const external = doctorPage.getByRole("group", {
      name: "Внешняя оценка 1",
      exact: true,
    });
    await expect(external.getByLabel("Сумма", { exact: false })).toHaveValue(
      "",
    );
    await external.getByLabel("Сумма", { exact: false }).fill("8");
    await external
      .getByLabel("Источник внешней оценки", { exact: true })
      .fill("Оценка PHQ-9 из внешнего врачебного осмотра");
    await doctorPage
      .getByRole("button", {
        name: "Сохранить новую версию контекста",
        exact: true,
      })
      .click();
    await expect(
      doctorPage.getByText("Клинический контекст сохранён новой версией.", {
        exact: true,
      }),
    ).toBeVisible();
    const initial = await (
      await doctor.get(`/api/v1/patients/${patientID}/clinical-context`)
    ).json();
    expect(initial.version).toBe(1);
    expect(initial.context.diagnoses).toBe(privateDiagnosis);
    await doctorPage
      .getByRole("heading", {
        name: "История клинического контекста",
        exact: true,
      })
      .scrollIntoViewIfNeeded();
    await expect(
      doctorPage.locator("summary").filter({ hasText: "Версия 1" }),
    ).toBeVisible();
    await doctorPage.getByRole("tab", { name: "Голос", exact: true }).click();
    const audio = doctorPage.locator(`#audio-${audioID}`);
    await audio
      .getByRole("button", {
        name: "Клинические отчёты по записи",
        exact: true,
      })
      .click();
    await expect(
      audio
        .getByText(/Демонстрационный режим — результат модели синтетический/)
        .first(),
    ).toBeVisible();
    await audio
      .getByRole("button", { name: "Создать клинический отчёт", exact: true })
      .click();
    const reportsPath = `/api/v1/patients/${patientID}/audio/${audioID}/clinical-reports`;
    let savedReport: ClinicalReport | undefined;
    await expect
      .poll(
        async () => {
          const result = await (await doctor.get(reportsPath)).json();
          savedReport = result.items[0] as ClinicalReport | undefined;
          return savedReport?.model_status;
        },
        { timeout: 60000 },
      )
      .toBe("ready");
    expect(savedReport?.model_result?.is_mock).toBe(true);
    expect(savedReport?.source_snapshot.context_version?.version).toBe(1);
    const report = audio.getByTestId(`clinical-report-${savedReport!.id}`);
    await expect(
      report.getByRole("heading", {
        name: "Демонстрационные выходы модели",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      report.getByRole("table", {
        name: "Синтетические значения для демонстрации интерфейса",
        exact: true,
      }),
    ).toBeVisible({ timeout: 20000 });
    await expect(
      report
        .getByRole("table", {
          name: "Синтетические значения для демонстрации интерфейса",
          exact: true,
        })
        .getByRole("row"),
    ).toHaveCount(5);
    await expect(
      report
        .getByText(/Демонстрационный режим — результат модели синтетический/)
        .first(),
    ).toBeVisible();
    await report
      .locator("summary")
      .filter({ hasText: "Клинические, акустические и дневниковые источники" })
      .click();
    await expect(
      report.getByText(privateDiagnosis, { exact: true }),
    ).toBeVisible();
    const conclusion = `Авторское заключение ${unique}`;
    await report
      .getByLabel("Авторское заключение врача", { exact: true })
      .fill(conclusion);
    await report
      .getByRole("button", { name: "Сохранить заключение врача", exact: true })
      .click();
    await expect(
      report.getByText("Заключение врача сохранено с автором и датой.", {
        exact: true,
      }),
    ).toBeVisible();
    await expect
      .poll(
        async () =>
          (await (await doctor.get(`${reportsPath}/${savedReport!.id}`)).json())
            .conclusions.length,
      )
      .toBe(1);
    await expect(report.getByText(conclusion, { exact: true })).toBeVisible();
    const second = await doctor.post(
      `/api/v1/patients/${patientID}/clinical-context`,
      {
        headers: { "X-CSRF-Token": doctorSession.csrf },
        data: {
          base_version: 1,
          request_key: randomUUID(),
          context: {
            ...initial.context,
            diagnoses: `Обновлённая версия ${unique}`,
          },
        },
      },
    );
    expect(second.status()).toBe(201);
    const stale = await doctor.post(
      `/api/v1/patients/${patientID}/clinical-context`,
      {
        headers: { "X-CSRF-Token": doctorSession.csrf },
        data: {
          base_version: 1,
          request_key: randomUUID(),
          context: initial.context,
        },
      },
    );
    expect(stale.status()).toBe(409);
    const historicalReport = await (
      await doctor.get(`${reportsPath}/${savedReport!.id}`)
    ).json();
    expect(
      historicalReport.source_snapshot.context_version.context.diagnoses,
    ).toBe(privateDiagnosis);
    for (const path of [
      `/api/v1/patients/${patientID}/clinical-context`,
      `/api/v1/patients/${patientID}/clinical-context/history`,
      reportsPath,
      `${reportsPath}/${savedReport!.id}`,
      "/api/v1/voicefm/status",
    ])
      expect((await page.request.get(path)).status()).toBe(403);
    expect(
      (
        await page.request.post(
          `${reportsPath}/${savedReport!.id}/conclusions`,
          {
            headers: { Origin: baseURL, "X-CSRF-Token": patientSession.csrf },
            data: { request_key: randomUUID(), text: "Недопустимо" },
          },
        )
      ).status(),
    ).toBe(403);
    const raw = await (
      await page.request.get(`/api/v1/patients/${patientID}/audio`)
    ).text();
    expect(raw).not.toContain(privateDiagnosis);
    expect(raw).not.toContain(conclusion);
    expect(raw).not.toContain("voicefm-demo");
    expect(
      await (await page.request.get("/api/v1/dashboard")).text(),
    ).not.toContain(privateDiagnosis);
    await page.getByRole("button", { name: "Голос", exact: true }).click();
    await expect(page.locator(`#audio-${audioID}`)).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Клинические отчёты по записи",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("tab", { name: "Клинический контекст", exact: true }),
    ).toHaveCount(0);
    await expect(page.getByText(privateDiagnosis, { exact: true })).toHaveCount(
      0,
    );
    await doctorPage.route(
      `**/api/v1/patients/${patientID}/audio/${audioID}/clinical-reports?*`,
      (r) =>
        r.fulfill({ status: 403, json: { error: "Доступ к отчётам отозван" } }),
    );
    await doctorPage.evaluate(() =>
      window.dispatchEvent(new Event("data-refresh")),
    );
    await expect(
      doctorPage.getByText("Доступ к отчётам отозван", { exact: true }),
    ).toBeVisible();
    await expect(
      doctorPage.getByTestId(`clinical-report-${savedReport!.id}`),
    ).toHaveCount(0);
    await expect(doctorPage.getByText(conclusion, { exact: true })).toHaveCount(
      0,
    );
  } finally {
    await doctorBrowser.close();
    await doctor.dispose();
  }
});
