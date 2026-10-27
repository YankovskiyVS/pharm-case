import { test, expect } from "@playwright/test";
import { createHmac, randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { LoginPage } from "./pages";
import type { Model } from "../packages/api-client";

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
    count = 6 * rate;
  const wav = Buffer.alloc(44 + count * 2);
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

test("patient uploads audio; real acoustic worker measures it; doctor can play it; another patient cannot", async ({
  page,
  browser,
  baseURL,
}) => {
  test.setTimeout(150000);
  const login = new LoginPage(page, "patient");
  await login.open();
  await login.login("patient3@psitech.local", password);
  await page.getByRole("button", { name: "Голос", exact: true }).click();
  // A patient may choose a file before entering any metadata or consent.
  await page.getByLabel("Или загрузить аудиофайл").setInputFiles({
    name: "synthetic-180hz.wav",
    mimeType: "audio/vnd.wave",
    buffer: toneWav(),
  });
  await expect(page.getByText(/Для отправки укажите устройство/)).toBeVisible();
  await expect(page.getByLabel("Устройство и микрофон")).toBeEnabled();
  await expect(page.getByLabel("Задание", { exact: true })).toBeEnabled();
  await page.getByLabel("Задание", { exact: true }).selectOption("vowel");
  await page
    .getByLabel("Устройство и микрофон")
    .fill(`Synthetic tone ${randomUUID()}`);
  await page.getByRole("checkbox", { name: /Я согласен/ }).check();
  await expect(
    page.getByRole("button", { name: "Отправить на анализ" }),
  ).toBeEnabled();
  const uploaded = page.waitForResponse(
    (r) =>
      r.url().includes("/api/v1/patient/audio?") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Отправить на анализ" }).click();
  const response = await uploaded;
  expect(response.request().headers()["content-type"]).toBe("audio/wav");
  expect(response.status()).toBe(201);
  const { id } = (await response.json()) as { id: string };
  const me = await (await page.request.get("/api/v1/me")).json();
  const patientID = String(me.user.id);
  await expect
    .poll(
      async () => {
        const res = await page.request.get(
          `/api/v1/patients/${patientID}/audio`,
        );
        const data = (await res.json()) as Model<"AudioList">;
        return data.recordings.find((r) => r.id === id)?.status;
      },
      { timeout: 90000, intervals: [1000, 2000] },
    )
    .toBe("ready");
  const data = (await (
    await page.request.get(`/api/v1/patients/${patientID}/audio`)
  ).json()) as Model<"AudioList">;
  const measured = data.recordings.find((r) => r.id === id)!;
  expect(measured.result?.usable).toBe(true);
  expect(measured.result?.metrics.f0_median_hz).toBeCloseTo(180, 0);
  await expect(page.locator(`#audio-${id}`)).toContainText("Обработано", {
    timeout: 20000,
  });

  const doctor = await browser.newContext();
  const other = await browser.newContext();
  try {
    const doctorPage = await doctor.newPage();
    const doctorLogin = new LoginPage(doctorPage, "doctor");
    await doctorLogin.open();
    await doctorLogin.login("doctor@psitech.local", password, doctorCode());
    await doctorPage.getByLabel("Поиск пациента").fill("Дарья Орлова");
    await doctorPage
      .getByRole("button", { name: "Открыть профиль Дарья Орлова" })
      .click();
    await doctorPage.getByRole("tab", { name: "Голос", exact: true }).click();
    await expect(doctorPage.locator(`#audio-${id}`)).toContainText(
      "Медиана основного тона",
    );
    expect(
      (
        await doctorPage.request.get(
          `/api/v1/patients/${patientID}/audio/${id}/file`,
        )
      ).status(),
    ).toBe(200);
    const session = await other.request.post(`${baseURL}/api/v1/auth/login`, {
      headers: { Origin: baseURL! },
      data: {
        email: "patient4@psitech.local",
        password,
        portal: "patient",
        code: "",
      },
    });
    expect(session.status()).toBe(200);
    expect(
      (
        await other.request.get(
          `${baseURL}/api/v1/patients/${patientID}/audio/${id}/file`,
        )
      ).status(),
    ).toBe(403);
  } finally {
    await doctor.close();
    await other.close();
  }
});
